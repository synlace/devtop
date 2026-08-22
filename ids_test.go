package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMintArtifactID_MonotoneAndNeverReused(t *testing.T) {
	setupMultiWorkflowEnv(t)
	p := defaultPaths()

	if id := mintArtifactID(engineConfig, p, "intents"); id != "INT-1" {
		t.Fatalf("first mint = %q, want INT-1", id)
	}
	// The mint only allocates; the caller writes the file. Delete what a
	// handler would have written: the id must not be recycled.
	writeArtifact(t, "intents/INT-1.mdx", "---\ntitle: \"X\"\n---\n\nB\n")
	writeArtifact(t, "intents/INT-2.mdx", "---\ntitle: \"Y\"\n---\n\nB\n")
	if err := os.Remove(filepath.Join(p.DevTop, "intents", "INT-1.mdx")); err != nil {
		t.Fatal(err)
	}
	if id := mintArtifactID(engineConfig, p, "intents"); id != "INT-3" {
		t.Fatalf("mint after delete = %q, want INT-3 (never reuse)", id)
	}
}

func TestMintArtifactID_SeesCommittedFiles(t *testing.T) {
	setupMultiWorkflowEnv(t)
	p := defaultPaths()
	// Committed (or hand-edited) ids are the floor, whatever their padding:
	// REQ-007 on disk means the next mint is REQ-8.
	writeArtifact(t, "requirements/REQ-007.mdx", "---\ntitle: \"R\"\nwork_item: INT-1\n---\n\nB\n")
	if id := mintArtifactID(engineConfig, p, "requirements"); id != "REQ-8" {
		t.Fatalf("mint over committed REQ-007 = %q, want REQ-8", id)
	}
	if id := mintArtifactID(engineConfig, p, "requirements"); id != "REQ-9" {
		t.Fatalf("next mint = %q, want REQ-9", id)
	}
}

func TestMintArtifactID_UnpaddedAcrossKinds(t *testing.T) {
	setupMultiWorkflowEnv(t)
	p := defaultPaths()
	if id := mintArtifactID(engineConfig, p, "bugs"); id != "BUG-1" {
		t.Fatalf("bug mint = %q, want BUG-1", id)
	}
	if id := mintArtifactID(engineConfig, p, "chores"); id != "CHORE-1" {
		t.Fatalf("chore mint = %q, want CHORE-1", id)
	}
	if id := mintArtifactID(engineConfig, p, "tickets"); id != "1" {
		t.Fatalf("ticket mint = %q, want 1", id)
	}
}

func TestStoreLock_ExcludesSecondHolder(t *testing.T) {
	setupMultiWorkflowEnv(t)
	lockPath := storeLockPath(defaultPaths())
	// flock locks belong to open file descriptions: a second open in the
	// same process contends exactly like a second process would.
	f1, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f1.Close()
	if err := syscall.Flock(int(f1.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	f2, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if err := syscall.Flock(int(f2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("second holder acquired the store lock while it was held")
	}
	if err := syscall.Flock(int(f1.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock not re-acquirable after release: %v", err)
	}
}

// TestStoreLock_CrossProcess: a child process (the same test binary) cannot
// mint while the parent holds the store lock, and can after release.
func TestStoreLock_CrossProcess(t *testing.T) {
	setupMultiWorkflowEnv(t)
	lockPath := storeLockPath(defaultPaths())
	dir := DEVTOP_DIR

	hold, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if err := syscall.Flock(int(hold.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "DEVTOP_LOCK_HELPER=1", "DEVTOP_LOCK_HELPER_DIR="+dir)
	run := func() (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelper$")
		cmd.Env = env
		var buf bytes.Buffer
		cmd.Stderr = &buf
		err := cmd.Run()
		return buf.String(), err
	}
	log, err := run()
	if err == nil {
		t.Fatalf("child minted while the parent held the store lock: %s", log)
	}
	if err := syscall.Flock(int(hold.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	log, err = run()
	if err != nil {
		t.Fatalf("child could not mint after the lock was released: %v (%s)", err, log)
	}
	if !strings.Contains(log, "helper minted") {
		t.Fatalf("helper did not mint: %s", log)
	}
}

// TestStoreLockHelper is the child-side of TestStoreLock_CrossProcess: it
// mints one requirement id after acquiring the store lock, exiting 1 when
// the lock is held by the parent.
// TestStoreLockHelper is the child-side of TestStoreLock_CrossProcess. It
// first probes the store lock without blocking: held by the parent, it exits
// 1 ("blocked"); free, it acquires briefly, then mints one requirement id.
func TestStoreLockHelper(t *testing.T) {
	if os.Getenv("DEVTOP_LOCK_HELPER") != "1" {
		t.Skip("helper process only")
	}
	DEVTOP_DIR = os.Getenv("DEVTOP_LOCK_HELPER_DIR")
	docs := filepath.Join(DEVTOP_DIR, "docs")
	tickets := filepath.Join(DEVTOP_DIR, "tickets")
	threads := filepath.Join(DEVTOP_DIR, "threads")
	data := filepath.Join(DEVTOP_DIR, "data")
	if err := os.MkdirAll(data, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "helper mkdir: %v\n", err)
		os.Exit(1)
	}
	DOCS_DIR, TICKETS_DIR, THREADS_DIR, DATA_DIR = docs, tickets, threads, data
	probe, err := os.OpenFile(storeLockPath(defaultPaths()), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper open: %v\n", err)
		os.Exit(1)
	}
	defer probe.Close()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintf(os.Stderr, "helper blocked\n")
		os.Exit(1)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_UN); err != nil {
		fmt.Fprintf(os.Stderr, "helper unlock: %v\n", err)
		os.Exit(1)
	}
	engineConfig = multiWorkflowCfg()
	id := mintArtifactID(engineConfig, defaultPaths(), "requirements")
	if id == "" {
		fmt.Fprintf(os.Stderr, "helper: mint returned empty\n")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "helper minted %s\n", id)
}

func TestLessArtifactID_NumericOrder(t *testing.T) {
	pairs := [][2]string{
		{"REQ-2", "REQ-10"}, {"REQ-10", "REQ-100"}, {"REQ-1", "REQ-2"},
		{"INT-2", "INT-10"}, {"DEC-3", "DEC-12"}, {"BUG-1", "BUG-2"},
		{"REQ-001", "REQ-2"}, {"1", "2"}, {"9", "10"},
	}
	for _, p := range pairs {
		if !lessArtifactID(p[0], p[1]) {
			t.Errorf("lessArtifactID(%q, %q) = false, want true", p[0], p[1])
		}
		if lessArtifactID(p[1], p[0]) {
			t.Errorf("lessArtifactID(%q, %q) = true, want false", p[1], p[0])
		}
	}
}
