package core

import (
	"bytes"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Change struct {
	Path          string
	Before, After []byte
	Mode          fs.FileMode
	Exists        bool
}

// Plan prepares and reparses every changed file before any disk writes occur.
func Plan(r *Result) ([]Change, error) {
	for _, row := range r.Rows {
		if strings.HasPrefix(row.Status, "エラー") || row.Status == "未処理" {
			return nil, fmt.Errorf("scan did not finish successfully")
		}
	}
	var changes []Change
	for _, p := range r.packages {
		if len(p.counts) == 0 {
			continue
		}
		for _, s := range p.files {
			if len(s.literals) == 0 {
				continue
			}
			var out bytes.Buffer
			offset := 0
			for _, lit := range s.literals {
				out.Write(s.data[offset:lit.start])
				out.WriteString(p.values[lit.value])
				offset = lit.end
			}
			out.Write(s.data[offset:])
			changes = append(changes, Change{s.path, s.data, out.Bytes(), s.mode, true})
		}
		var out bytes.Buffer
		fmt.Fprintf(&out, "%s\n\npackage %s\n\nconst (\n", Marker, p.name)
		names := map[string]string{}
		var sorted []string
		for value, name := range p.values {
			names[name] = value
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			fmt.Fprintf(&out, "\t%s = %s\n", name, strconv.Quote(names[name]))
		}
		out.WriteString(")\n")
		data, err := format.Source(out.Bytes())
		if err != nil {
			return nil, err
		}
		c := Change{Path: filepath.Join(p.dir, GeneratedName), After: data, Mode: 0644}
		if p.old != nil {
			c.Before = p.old.data
			c.Mode = p.old.mode
			c.Exists = true
			if bytes.Contains(c.Before, []byte("\r\n")) {
				c.After = bytes.ReplaceAll(c.After, []byte("\n"), []byte("\r\n"))
			}
			if !bytes.HasSuffix(c.Before, []byte("\n")) {
				c.After = bytes.TrimSuffix(c.After, []byte("\r\n"))
				c.After = bytes.TrimSuffix(c.After, []byte("\n"))
			}
		}
		if !bytes.Equal(c.Before, c.After) {
			changes = append(changes, c)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	for _, c := range changes {
		if _, err := parser.ParseFile(token.NewFileSet(), c.Path, c.After, parser.AllErrors); err != nil {
			return nil, fmt.Errorf("rewrite validation %s: %w", c.Path, err)
		}
	}
	return changes, nil
}

type fileOps struct {
	rename func(string, string) error
	remove func(string) error
}

// Commit stages all output and backups first. An ordinary write failure rolls
// back to the immediately preceding contents, not an earlier strsweep run.
func Commit(changes []Change) error { return commit(changes, fileOps{os.Rename, os.Remove}) }
func commit(changes []Change, ops fileOps) error {
	type staged struct {
		change       Change
		temp, backup string
		applied      bool
	}
	var stagedFiles []*staged
	cleanup := func() {
		for _, s := range stagedFiles {
			if s.temp != "" {
				_ = os.Remove(s.temp)
			}
			if s.backup != "" {
				_ = os.Remove(s.backup)
			}
		}
	}
	check := func(c Change) error {
		// Never follow a symlink introduced between scanning and committing.
		dir := filepath.Dir(c.Path)
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if resolved != dir {
			return fmt.Errorf("symlink parent: %s", dir)
		}
		info, err := os.Lstat(c.Path)
		if !c.Exists {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("destination appeared since scan: %s", c.Path)
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", c.Path)
		}
		data, err := os.ReadFile(c.Path)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, c.Before) || info.Mode() != c.Mode {
			return fmt.Errorf("file changed since scan: %s", c.Path)
		}
		return nil
	}
	stage := func(path string, data []byte, mode fs.FileMode) (string, error) {
		f, e := os.CreateTemp(filepath.Dir(path), ".strsweep-*")
		if e != nil {
			return "", e
		}
		name := f.Name()
		_, e = f.Write(data)
		if e == nil {
			e = f.Chmod(mode)
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			_ = os.Remove(name)
			return "", e
		}
		return name, nil
	}
	for _, c := range changes {
		if e := check(c); e != nil {
			cleanup()
			return e
		}
		s := &staged{change: c}
		stagedFiles = append(stagedFiles, s)
		var e error
		s.temp, e = stage(c.Path, c.After, c.Mode)
		if e != nil {
			cleanup()
			return e
		}
		if c.Exists {
			s.backup, e = stage(c.Path, c.Before, c.Mode)
			if e != nil {
				cleanup()
				return e
			}
		}
	}
	rollback := func(cause error) error {
		var failures []string
		for i := len(stagedFiles) - 1; i >= 0; i-- {
			s := stagedFiles[i]
			if s.applied {
				var e error
				if s.change.Exists {
					e = ops.rename(s.backup, s.change.Path)
					if e == nil {
						s.backup = ""
					}
				} else {
					e = ops.remove(s.change.Path)
				}
				if e != nil {
					failures = append(failures, fmt.Sprintf("%s: %v (backup: %s)", s.change.Path, e, s.backup))
				}
			}
		}
		if len(failures) > 0 {
			for _, s := range stagedFiles {
				if s.temp != "" {
					_ = os.Remove(s.temp)
				}
			}
			return fmt.Errorf("%w; recovery failed; retained backups: %s", cause, strings.Join(failures, "; "))
		}
		cleanup()
		return cause
	}
	for _, s := range stagedFiles {
		if e := check(s.change); e != nil {
			return rollback(e)
		}
		if e := ops.rename(s.temp, s.change.Path); e != nil {
			return rollback(e)
		}
		s.temp = ""
		s.applied = true
	}
	cleanup()
	return nil
}
