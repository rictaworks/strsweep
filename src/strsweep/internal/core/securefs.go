package core

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// dirHandle keeps all mutations relative to a pinned directory, never an
// absolute path that an intervening symlink can redirect elsewhere.
type dirHandle struct {
	root *os.Root
	path string
	info fs.FileInfo
}

func securePlatform() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("strsweep supports Linux only (current platform: %s)", runtime.GOOS)
	}
	return nil
}
func applyPlatform() error { return securePlatform() }

// Bootstrap from a pinned volume root. Every component is selected through
// its parent's handle, so an ancestor symlink swap cannot redirect OpenRoot.
func pinRoot(path string, expected fs.FileInfo) (*dirHandle, error) {
	if err := securePlatform(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("root must be absolute: %s", path)
	}
	volume := filepath.VolumeName(path)
	base := volume + string(filepath.Separator)
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), base), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		before, e := root.Lstat(part)
		if e != nil {
			root.Close()
			return nil, e
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("non-directory or symlink root component: %s", part)
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			root.Close()
			return nil, e
		}
		actual, e := next.Stat(".")
		root.Close()
		if e != nil || !os.SameFile(before, actual) {
			next.Close()
			return nil, fmt.Errorf("root component changed while opening: %s", part)
		}
		root = next
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	if expected != nil && !os.SameFile(expected, info) {
		root.Close()
		return nil, fmt.Errorf("root directory changed since scan: %s", path)
	}
	d := &dirHandle{root, path, info}
	if err = d.checkPath(); err != nil {
		root.Close()
		return nil, err
	}
	return d, nil
}
func (d *dirHandle) checkPath() error {
	resolved, err := filepath.EvalSymlinks(d.path)
	if err != nil {
		return err
	}
	if resolved != d.path {
		return fmt.Errorf("symlink directory replacement: %s", d.path)
	}
	info, err := os.Lstat(d.path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !os.SameFile(d.info, info) {
		return fmt.Errorf("directory changed since scan: %s", d.path)
	}
	return nil
}
func noSymlinks(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	if !filepath.IsLocal(name) {
		return fmt.Errorf("path escapes root: %s", name)
	}
	part := ""
	for _, p := range strings.Split(name, string(filepath.Separator)) {
		part = filepath.Join(part, p)
		info, err := root.Lstat(part)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink excluded: %s", name)
		}
	}
	return nil
}
func (d *dirHandle) child(name string, expected fs.FileInfo) (*dirHandle, error) {
	if err := noSymlinks(d.root, name); err != nil {
		return nil, err
	}
	before, err := d.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if expected != nil && !os.SameFile(before, expected) {
		return nil, fmt.Errorf("package directory changed since scan: %s", name)
	}
	root, err := d.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(before, info) {
		root.Close()
		return nil, fmt.Errorf("package directory changed while opening: %s", name)
	}
	child := &dirHandle{root, filepath.Join(d.path, name), info}
	if err = child.checkPath(); err != nil {
		root.Close()
		return nil, err
	}
	return child, nil
}
func readRegular(root *os.Root, name string) ([]byte, fs.FileInfo, error) {
	if err := noSymlinks(root, name); err != nil {
		return nil, nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a regular file: %s", name)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|nonblockFlag(), 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, nil, fmt.Errorf("file changed while opening: %s", name)
	}
	data, err := io.ReadAll(f)
	return data, info, err
}
