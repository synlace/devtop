package main

import (
	"fmt"
	"sync"
	"time"
)

// Run guards: the loop guard and the idle watchdog. They exist so a stuck
// or flailing run reaches a terminal event instead of spinning — the run
// trace is a debugging unit, and a run that never ends is one that never
// gets debugged.

// Thresholds. A normal derive makes a handful of tool calls; the constants
// are generous enough for legitimate edit-then-rewrite patterns and small
// enough that the guard fires before the user gives up waiting.
const (
	loopFlagAt       = 3 // first suspicious repeat: emit run.flag
	loopFlagAgain    = 6 // second suspicious repeat: emit run.flag again
	loopAbortRepeats = 8 // same tool + identical args: abort the run
	errorBudget      = 5 // consecutive failing tool calls: abort the run
	watchHeartbeat   = 30 * time.Second
	idleAbort        = 10 * time.Minute
)

var (
	watchdogOnce sync.Once
)

func ensureWatchdog() {
	watchdogOnce.Do(func() {
		go watchdogLoop()
	})
}

func watchdogLoop() {
	t := time.NewTicker(watchHeartbeat)
	defer t.Stop()
	for now := range t.C {
		var victims []*Run
		activeRuns.mu.Lock()
		for _, r := range activeRuns.m {
			r.mu.Lock()
			idle := now.Sub(r.last)
			terminal := r.terminal
			r.mu.Unlock()
			if !terminal && idle > idleAbort {
				victims = append(victims, r)
			}
		}
		activeRuns.mu.Unlock()
		for _, r := range victims {
			r.Abort(fmt.Sprintf("no run activity for %s (watchdog abort)", idleAbort))
		}
	}
}
