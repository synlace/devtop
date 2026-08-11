//go:build linux

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// landlockRw is the full write surface git and the file tools need under
	// the repo root (REFER covers cross-directory renames, e.g. git objects).
	// TRUNCATE is ABI v3 (kernel 6.2+); the fallback below drops it.
	landlockRw = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_REFER |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE
	// landlockRo allows reading and executing everywhere, so git and the
	// tools keep working without being able to modify anything outside the
	// rw roots.
	landlockRo = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_EXECUTE
)

// landlockAvailable reports whether the kernel supports Landlock. A NULL
// attr returns EFAULT even on supported kernels, so probe with a minimal
// valid ruleset instead.
func landlockAvailable() bool {
	attr := unix.LandlockRulesetAttr{Access_fs: landlockRo}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return false
	}
	unix.Close(int(fd))
	return true
}

// applyLandlock restricts the current process: reads and executes stay
// allowed everywhere, but writes are only possible under the given roots.
func applyLandlock(rwRoots ...string) error {
	if !landlockAvailable() {
		return fmt.Errorf("landlock unavailable")
	}
	candidates := []uint64{landlockRw | landlockRo}
	// Older ABIs reject newer access bits; retry without REFER (5.19) and
	// then without TRUNCATE (6.2).
	candidates = append(candidates, (landlockRw&^(unix.LANDLOCK_ACCESS_FS_REFER|unix.LANDLOCK_ACCESS_FS_TRUNCATE) | landlockRo))
	candidates = append(candidates, (landlockRw&^unix.LANDLOCK_ACCESS_FS_TRUNCATE | landlockRo))

	var rulesetFd int
	for _, access := range candidates {
		attr := unix.LandlockRulesetAttr{Access_fs: access}
		fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
		if errno != 0 {
			continue
		}
		rulesetFd = int(fd)
		break
	}
	if rulesetFd == 0 {
		return fmt.Errorf("landlock ruleset creation failed")
	}
	defer unix.Close(rulesetFd)
	var errno syscall.Errno

	addRule := func(root string, access uint64) error {
		dirFd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer unix.Close(dirFd)
		rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(dirFd)}
		_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_ADD_RULE,
			uintptr(rulesetFd), uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafe.Pointer(&rule)))
		if errno != 0 {
			return errno
		}
		return nil
	}

	if err := addRule("/", landlockRo); err != nil {
		return err
	}
	for _, root := range rwRoots {
		if root == "" {
			continue
		}
		if err := addRule(root, landlockRw); err != nil {
			return err
		}
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFd), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
