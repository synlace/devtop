package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func captureEvents(l *eventLog, after int64, n int) []RunEvent {
	ch, id := l.subscribe(after)
	defer l.unsubscribe(id)
	var got []RunEvent
	for len(got) < n {
		ev, ok := <-ch
		if !ok {
			break
		}
		got = append(got, ev)
	}
	return got
}

func resetGlobalLog(t *testing.T, dir string) {
	t.Helper()
	theLog.mu.Lock()
	theLog.file = nil
	theLog.path = ""
	theLog.evs = nil
	theLog.subs = map[int64]chan RunEvent{}
	theLog.next = 1
	theLog.mu.Unlock()
	if err := theLog.init(filepath.Join(dir, "events.log")); err != nil {
		t.Fatalf("init: %v", err)
	}
}

func TestEventLogEmitReplayOrder(t *testing.T) {
	log := &eventLog{subs: map[int64]chan RunEvent{}, next: 1}
	if err := log.init(filepath.Join(t.TempDir(), "events.log")); err != nil {
		t.Fatalf("init: %v", err)
	}

	emitRunEventOn := func(l *eventLog, typ string, payload interface{}) {
		var raw json.RawMessage
		if payload != nil {
			b, _ := json.Marshal(payload)
			raw = b
		}
		l.emit(RunEvent{Type: typ, Payload: raw})
	}
	emitRunEventOn(log, "seed.created", map[string]interface{}{"id": "INT-1"})
	emitRunEventOn(log, "derive.done", map[string]interface{}{"slug": "x"})
	emitRunEventOn(log, "workitem.published", map[string]interface{}{"id": "WI-1"})

	// Full replay: all three, in order, with sequential seqs.
	got := captureEvents(log, 0, 3)
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	wantTypes := []string{"seed.created", "derive.done", "workitem.published"}
	for i, ev := range got {
		if ev.Type != wantTypes[i] {
			t.Errorf("event %d type = %q, want %q", i, ev.Type, wantTypes[i])
		}
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
		if ev.TS == "" {
			t.Errorf("event %d missing timestamp", i)
		}
	}

	// Replay after the first event skips it.
	got = captureEvents(log, 1, 2)
	if len(got) != 2 || got[0].Type != "derive.done" {
		t.Fatalf("after-cursor replay wrong: %+v", got)
	}
}

func TestEventLogDurableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	log1 := &eventLog{subs: map[int64]chan RunEvent{}, next: 1}
	if err := log1.init(path); err != nil {
		t.Fatalf("init: %v", err)
	}
	log1.emit(RunEvent{Type: "derive.done", Scope: "requirements"})
	log1.emit(RunEvent{Type: "seed.created", Scope: "intents"})

	// A fresh instance over the same file must replay history.
	log2 := &eventLog{subs: map[int64]chan RunEvent{}, next: 1}
	if err := log2.init(path); err != nil {
		t.Fatalf("reinit: %v", err)
	}
	got := captureEvents(log2, 0, 2)
	if len(got) != 2 || got[1].Type != "seed.created" {
		t.Fatalf("restart replay wrong: %+v", got)
	}

	// New events continue the sequence across the restart boundary.
	log2.emit(RunEvent{Type: "workitem.published"})
	got = captureEvents(log2, 2, 1)
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("post-restart seq = %+v, want seq 3", got)
	}
}

func TestEventStreamReplayOnce(t *testing.T) {
	dir := t.TempDir()
	resetGlobalLog(t, dir)
	theLog.emit(RunEvent{Type: "seed.created", Scope: "intents", TS: ""})
	theLog.emit(RunEvent{Type: "derive.done", Scope: "requirements", TS: ""})

	req := httptest.NewRequest("GET", "/api/events?after=0&once=1", nil)
	rr := httptest.NewRecorder()
	handleAPIEventStream(rr, req)

	body := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(body, "id: 1") || !strings.Contains(body, "seed.created") {
		t.Errorf("replay missing first event: %q", body)
	}
	if !strings.Contains(body, "id: 2") || !strings.Contains(body, "derive.done") {
		t.Errorf("replay missing second event: %q", body)
	}
	if strings.Contains(body, "ping") {
		t.Errorf("heartbeat leaked into replay-only response: %q", body)
	}
}
