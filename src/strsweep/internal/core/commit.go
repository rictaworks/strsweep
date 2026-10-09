package core

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Hooks run before the real, handle-relative operations. They never replace
// secure filesystem operations and exist only for deterministic failure tests.
type fileOps struct {
	afterCheck   func(string) error
	beforeRename func(string, string) error
	beforeRemove func(string) error
	beforeLink   func(string, string) error
}

func Commit(changes []Change) error { return commit(changes, fileOps{}) }
func commit(changes []Change, ops fileOps) error {
	if len(changes) == 0 {
		return nil
	}
	if err := applyPlatform(); err != nil {
		return err
	}
	type staged struct {
		change             Change
		dir                *dirHandle
		name, temp, backup string
		applied            bool
		installed          fs.FileInfo
	}
	var files []*staged
	roots := map[string]*dirHandle{}
	dirs := map[string]*dirHandle{}
	defer func() {
		for _, d := range dirs {
			d.root.Close()
		}
		for _, r := range roots {
			r.root.Close()
		}
	}()
	// Open and verify all directory handles before creating any files.
	for _, c := range changes {
		path, err := filepath.Abs(c.Path)
		if err != nil {
			return err
		}
		c.Path = path
		rootPath := c.root
		if rootPath == "" {
			rootPath = filepath.Dir(path)
		}
		root := roots[rootPath]
		if root == nil {
			root, err = pinRoot(rootPath, c.rootInfo)
			if err != nil {
				return err
			}
			roots[rootPath] = root
		}
		rel, err := filepath.Rel(rootPath, filepath.Dir(path))
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("change outside root: %s", path)
		}
		d := dirs[filepath.Dir(path)]
		if d == nil {
			d, err = root.child(rel, c.dirInfo)
			if err != nil {
				return err
			}
			dirs[filepath.Dir(path)] = d
		}
		files = append(files, &staged{change: c, dir: d, name: filepath.Base(path)})
	}
	cleanup := func(keepBackups bool) {
		for _, s := range files {
			if s.temp != "" {
				_ = s.dir.root.Remove(s.temp)
			}
			if !keepBackups && s.backup != "" {
				_ = s.dir.root.Remove(s.backup)
			}
		}
	}
	check := func(s *staged) error {
		if err := s.dir.checkPath(); err != nil {
			return err
		}
		_, err := s.dir.root.Lstat(s.name)
		if !s.change.Exists {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("destination appeared since scan: %s", s.change.Path)
		}
		if err != nil {
			return err
		}
		data, actual, err := readRegular(s.dir.root, s.name)
		if err != nil {
			return err
		}
		mode := s.change.Mode
		if s.change.beforeMode != 0 {
			mode = s.change.beforeMode
		}
		if !bytes.Equal(data, s.change.Before) || actual.Mode() != mode || (s.change.sourceInfo != nil && !os.SameFile(s.change.sourceInfo, actual)) {
			return fmt.Errorf("file changed since scan: %s", s.change.Path)
		}
		return nil
	}
	stage := func(d *dirHandle, data []byte, mode fs.FileMode) (string, fs.FileInfo, error) {
		name := ".strsweep-" + rand.Text()
		f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", nil, err
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Chmod(mode)
		}
		if err == nil {
			err = f.Sync()
		}
		info, se := f.Stat()
		ce := f.Close()
		if err == nil {
			err = se
		}
		if err == nil {
			err = ce
		}
		if err != nil {
			_ = d.root.Remove(name)
			return "", nil, err
		}
		return name, info, nil
	}
	for _, s := range files {
		if err := check(s); err != nil {
			cleanup(false)
			return err
		}
		var err error
		s.temp, s.installed, err = stage(s.dir, s.change.After, s.change.Mode)
		if err != nil {
			cleanup(false)
			return err
		}
		if s.change.Exists {
			s.backup, _, err = stage(s.dir, s.change.Before, 0600)
			if err != nil {
				cleanup(false)
				return err
			}
		}
	}
	rollback := func(cause error) error {
		var messages []string
		keepBackups := false
		for i := len(files) - 1; i >= 0; i-- {
			s := files[i]
			if !s.applied {
				continue
			}
			recoveryDir := ".strsweep-recovery-" + rand.Text()
			recovery := filepath.Join(recoveryDir, "snapshot")
			recoveryPath := filepath.Join(s.dir.path, recovery)
			// A private container protects recovery data without chmodding the
			// captured inode, which may have hard links outside this tree.
			err := s.dir.root.Mkdir(recoveryDir, 0700)
			if err != nil {
				keepBackups = true
				messages = append(messages, fmt.Sprintf("recovery failed for %s: %v; backup: %s", s.change.Path, err, filepath.Join(s.dir.path, s.backup)))
				continue
			}
			recoveryHandle, e := s.dir.child(recoveryDir, nil)
			if e != nil {
				keepBackups = true
				messages = append(messages, fmt.Sprintf("recovery failed for %s: %v", s.change.Path, e))
				continue
			}
			// Hold this handle through all recovery, including exclusive
			// relinking of an editor's captured inode into the package.
			defer recoveryHandle.root.Close()
			if !privateRecoveryDirectory(recoveryHandle.info) {
				keepBackups = true
				messages = append(messages, "recovery directory is not private: "+recoveryHandle.path)
				continue
			}
			if ops.beforeRename != nil {
				err = ops.beforeRename(s.change.Path, recoveryPath)
			}
			if err == nil {
				err = s.dir.checkPath()
			}
			if err == nil {
				_, err = s.dir.root.Lstat(s.name)
			}
			if err == nil {
				err = recoveryRename(s.dir.root, s.name, recoveryHandle.root, "snapshot")
			}
			if err != nil {
				_ = s.dir.root.Remove(recoveryDir)
				keepBackups = true
				messages = append(messages, fmt.Sprintf("recovery failed for %s: %v; backup: %s", s.change.Path, err, filepath.Join(s.dir.path, s.backup)))
				continue
			}
			// Never delete this captured inode: an editor may still hold it open and
			// write after rollback returns. Its name is always reported for recovery.
			if e := recoveryHandle.checkPath(); e != nil {
				keepBackups = true
				messages = append(messages, "recovery snapshot retained in a moved directory; original location is no longer reliable: "+recoveryPath)
			} else {
				messages = append(messages, "recovery snapshot: "+recoveryPath)
			}
			data, info, readErr := readRegular(recoveryHandle.root, "snapshot")
			unchanged := readErr == nil && os.SameFile(s.installed, info) && bytes.Equal(data, s.change.After) && info.Mode() == s.change.Mode
			restore := s.backup
			restoreTemp := ""
			if unchanged && s.change.Exists {
				var e error
				restoreTemp, _, e = stage(s.dir, s.change.Before, s.change.originalMode())
				if e != nil {
					keepBackups = true
					messages = append(messages, fmt.Sprintf("recovery failed for %s: %v; backup: %s", s.change.Path, e, filepath.Join(s.dir.path, s.backup)))
					continue
				}
				restore = restoreTemp
			}
			if !unchanged {
				restore = recovery
				keepBackups = true
				messages = append(messages, "concurrent change preserved: "+s.change.Path)
			}
			if unchanged && !s.change.Exists {
				// The applied name is already absent; leave the captured inode for any
				// late writes through an open editor descriptor.
				if ops.beforeRemove != nil {
					if e := ops.beforeRemove(s.change.Path); e != nil {
						messages = append(messages, fmt.Sprintf("recovery failed for %s: %v", s.change.Path, e))
						keepBackups = true
					}
				}
				continue
			}
			if ops.beforeLink != nil {
				err = ops.beforeLink(filepath.Join(s.dir.path, restore), s.change.Path)
			}
			if err == nil {
				err = s.dir.checkPath()
			}
			if err == nil {
				if restore == recovery {
					err = recoveryLink(recoveryHandle.root, "snapshot", s.dir.root, s.name)
				} else {
					err = s.dir.root.Link(restore, s.name)
				}
			} // Exclusive: never overwrite another editor's new file.
			if err != nil {
				keepBackups = true
				messages = append(messages, fmt.Sprintf("recovery failed for %s: %v; backup: %s", s.change.Path, err, filepath.Join(s.dir.path, s.backup)))
			}
			if restoreTemp != "" {
				_ = s.dir.root.Remove(restoreTemp)
			}
		}
		if keepBackups {
			for _, s := range files {
				if s.backup != "" {
					messages = append(messages, fmt.Sprintf("backup for %s: %s", s.change.Path, filepath.Join(s.dir.path, s.backup)))
				}
			}
		}
		cleanup(keepBackups)
		if len(messages) > 0 {
			return fmt.Errorf("%w; %s", cause, strings.Join(messages, "; "))
		}
		return cause
	}
	for _, s := range files {
		if ops.beforeRename != nil {
			if err := ops.beforeRename(filepath.Join(s.dir.path, s.temp), s.change.Path); err != nil {
				return rollback(err)
			}
		}
		if err := check(s); err != nil {
			return rollback(err)
		}
		if ops.afterCheck != nil {
			if err := ops.afterCheck(s.change.Path); err != nil {
				return rollback(err)
			}
		}
		if err := s.dir.root.Rename(s.temp, s.name); err != nil {
			return rollback(err)
		}
		s.temp = ""
		s.applied = true
	}
	cleanup(false)
	return nil
}
func (c Change) originalMode() fs.FileMode {
	if c.beforeMode != 0 {
		return c.beforeMode
	}
	return c.Mode
}
