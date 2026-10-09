//go:build linux

package core

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Recovery crosses two already-pinned directory handles. Using a nested path
// through the package root would re-resolve a replaceable recovery-directory
// name and could follow an in-root symlink into a public directory.
func recoveryRename(from *os.Root, old string, to *os.Root, new string) error {
	if filepath.Base(old) != old || filepath.Base(new) != new {
		return fmt.Errorf("recovery names must be basenames")
	}
	a, err := from.Open(".")
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := to.Open(".")
	if err != nil {
		return err
	}
	defer b.Close()
	return syscall.Renameat(int(a.Fd()), old, int(b.Fd()), new)
}
func recoveryLink(from *os.Root, old string, to *os.Root, new string) error {
	if filepath.Base(old) != old || filepath.Base(new) != new {
		return fmt.Errorf("recovery names must be basenames")
	}
	a, err := from.Open(".")
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := to.Open(".")
	if err != nil {
		return err
	}
	defer b.Close()
	op, err := syscall.BytePtrFromString(old)
	if err != nil {
		return err
	}
	np, err := syscall.BytePtrFromString(new)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, a.Fd(), uintptr(unsafe.Pointer(op)), b.Fd(), uintptr(unsafe.Pointer(np)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func privateRecoveryDirectory(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode().Perm() == 0700 && int(st.Uid) == os.Geteuid()
}
