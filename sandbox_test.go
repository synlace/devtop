package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestApplyLandlock proves the OS-level containment: after applyLandlock, a
// process can write under the allowed root but cannot write elsewhere.
// Landlock is one-shot per process, so the assertions run in a child of the
// test binary; the parent only asserts the child's outcome.
func TestApplyLandlock(t *testing.T) {
	if os.Getenv("DEVTOP_LL_CHILD") == "1" {
		os.Exit(landlockAssertions())
	}
	if !landlockAvailable() {
		t.Skip("Landlock unavailable on this kernel/container")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestApplyLandlock$")
	cmd.Env = append(os.Environ(), "DEVTOP_LL_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("landlock assertions failed: %v\n%s", err, out)
	}
}

// landlockAssertions runs in a fresh process so the one-shot restriction
// cannot leak into the rest of the test binary.
func landlockAssertions() int {
	base, err := os.MkdirTemp("", "llbase-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := applyLandlock(root); err != nil {
		fmt.Fprintln(os.Stderr, "applyLandlock:", err)
		return 1
	}

	// Writes inside the allowed root succeed.
	in := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(in, []byte("x"), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "write inside allowed root:", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir inside allowed root:", err)
		return 1
	}

	// Writes outside the allowed root are denied by the kernel.
	out := filepath.Join(outside, "escape.txt")
	if err := os.WriteFile(out, []byte("x"), 0644); err == nil {
		fmt.Fprintln(os.Stderr, "write outside allowed root unexpectedly succeeded")
		return 1
	}
	if err := os.MkdirAll(filepath.Join(outside, "dir"), 0755); err == nil {
		fmt.Fprintln(os.Stderr, "mkdir outside allowed root unexpectedly succeeded")
		return 1
	}

	// An outside path reached through a symlink inside the root is denied too
	// (both the guard and the kernel reject it).
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		fmt.Fprintln(os.Stderr, "symlink:", err)
		return 1
	}
	if _, err := os.ReadFile(filepath.Join(link, "escape.txt")); err == nil {
		fmt.Fprintln(os.Stderr, "read through escaping symlink unexpectedly succeeded")
		return 1
	}
	return 0
}
