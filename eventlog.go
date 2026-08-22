package main

// Run-event log: a durable, replayable stream of engine lifecycle events.
//
// The log is append-only JSONL under .devtop/ so the UI — SSG or live — can
// replay history with Last-Event-ID and miss nothing while disconnected. The
// file is the durable source of truth; an in-process fan-out broadcasts to
// live SSE subscribers. No database dependency: the file is one line per
// event, swapped for SQLite later without changing the contract.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RunEvent is one engine lifecycle event. Seq is the replay cursor: an SSE
// client resumes at Last-Event-ID == Seq.
type RunEvent struct {
	Seq     int64           `json:"seq"`
	TS      string          `json:"ts"`
	Type    string          `json:"type"`
	Scope   string          `json:"scope,omitempty"`
	Run     string          `json:"run,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// eventLog holds the durable file plus the live subscriber fan-out.
// Emit and subscribe share the mutex so replay never races an append.
type eventLog struct {
	mu   sync.Mutex
	path string
	file *os.File
	evs  []RunEvent
	subs map[int64]chan RunEvent
	next int64
}

const maxMemEvents = 20000

// theLog is the process-wide stream (like the rest of this repo's singletons).
var theLog = &eventLog{subs: map[int64]chan RunEvent{}, next: 1}

// init opens (or creates) the JSONL file and rehydrates the in-memory window.
func (l *eventLog) init(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.path = path
	l.file = f
	in, rerr := os.Open(path)
	if rerr != nil {
		return nil
	}
	defer in.Close()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev RunEvent
		if json.Unmarshal(line, &ev) != nil || ev.Seq == 0 {
			continue
		}
		l.evs = append(l.evs, ev)
	}
	return nil
}

// ensureInit lazily points the global log at the primary repo's .devtop dir.
// A failed attach is loud, not silent: the stream keeps working in memory,
// but durability is lost until the next emit retries the attach.
func (l *eventLog) ensureInit() {
	l.mu.Lock()
	need := l.file == nil
	devtop := filepath.Join(defaultPaths().DevTop, "events.log")
	l.mu.Unlock()
	if need {
		if err := l.init(devtop); err != nil {
			fmt.Printf("Warning: event log not durable (%s): %v\n", devtop, err)
		}
	}
}

// emit appends one event durably and fans it out to live subscribers.
func (l *eventLog) emit(ev RunEvent) {
	l.ensureInit()
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := int64(0)
	if n := len(l.evs); n > 0 {
		prev = l.evs[n-1].Seq
	}
	ev.Seq = prev + 1
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if ev.Type == "" {
		return
	}
	n := len(l.evs)
	if n >= maxMemEvents {
		l.evs = append(l.evs[n-maxMemEvents+1:], ev)
	} else {
		l.evs = append(l.evs, ev)
	}
	if l.file != nil {
		if b, err := json.Marshal(ev); err == nil {
			_, _ = fmt.Fprintf(l.file, "%s\n", b)
		}
	}
	for _, ch := range l.subs {
		select {
		case ch <- ev:
		default:
			// Slow subscriber: skip rather than stall the engine.
		}
	}
}

// subscribe replays everything after the cursor, then registers for live
// events. Replay and registration happen atomically with respect to emit.
func (l *eventLog) subscribe(after int64) (<-chan RunEvent, int64) {
	l.ensureInit()
	l.mu.Lock()
	defer l.mu.Unlock()
	cap := 512
	if len(l.evs) > cap {
		cap = len(l.evs)
	}
	ch := make(chan RunEvent, cap)
	for _, ev := range l.evs {
		if ev.Seq > after {
			ch <- ev
		}
	}
	id := l.next
	l.next++
	l.subs[id] = ch
	return ch, id
}

func (l *eventLog) unsubscribe(id int64) {
	l.mu.Lock()
	if ch, ok := l.subs[id]; ok {
		delete(l.subs, id)
		close(ch)
	}
	l.mu.Unlock()
}

// emitRunEvent is the typed helper used by the engine hooks.
func emitRunEvent(evType, scope string, payload interface{}) {
	var raw json.RawMessage
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			raw = b
		}
	}
	theLog.emit(RunEvent{Type: evType, Scope: scope, Payload: raw})
}

// handleAPIEventStream serves the event stream with SSE replay:
//
//	GET /api/events?after=<seq>&once=1
//
// `after` resumes past the given sequence; without it, history replay starts
// at the newest event (live-only). `once=1` closes after the replay batch —
// useful for tooling and tests.
func handleAPIEventStream(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	once := r.URL.Query().Get("once") == "1"

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}

	ch, id := theLog.subscribe(after)
	defer theLog.unsubscribe(id)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data)
			flusher.Flush()
			if once {
				// Drain the replay batch, then stop.
				for {
					select {
					case more, ok := <-ch:
						if !ok {
							return
						}
						d, _ := json.Marshal(more)
						fmt.Fprintf(w, "id: %d\ndata: %s\n\n", more.Seq, d)
						flusher.Flush()
					default:
						return
					}
				}
			}
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// repoHeadSHA returns the repo's current short HEAD sha, or "" when the repo
// cannot resolve one (no commits yet, no git available).
func repoHeadSHA(repo *Repo) string {
	if repo == nil {
		return ""
	}
	repoDir := gitRootFrom(repo.paths.DevTop)
	if repoDir == "" {
		repoDir = repo.Root
	}
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = repoDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}
