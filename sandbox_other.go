//go:build !linux

package main

import "fmt"

// landlockAvailable reports whether the kernel supports Landlock.
func landlockAvailable() bool {
	return false
}

// applyLandlock restricts the current process: reads and executes stay
// allowed everywhere, but writes are only possible under the given roots.
func applyLandlock(rwRoots ...string) error {
	return fmt.Errorf("landlock unsupported on this platform")
}
