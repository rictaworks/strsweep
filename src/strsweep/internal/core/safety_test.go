//go:build linux

package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditConcurrentRollback(t *testing.T) {
	d := testDir(t)
	a := filepath.Join(d, "a.go")
	b := filepath.Join(d, "b.go")
	before := []byte("package p\nvar A = 1\n")
	after := []byte("package p\nvar A = 2\n")
	edit := []byte("package p\nvar A = 3 // editor update\n")
	os.WriteFile(a, before, 0600)
	os.WriteFile(b, before, 0600)
	n := 0
	err := commit([]Change{{Path: a, Before: before, After: after, Mode: 0600, Exists: true}, {Path: b, Before: before, After: after, Mode: 0600, Exists: true}}, fileOps{beforeRename: func(src, dst string) error {
		n++
		if n == 2 {
			os.WriteFile(a, edit, 0600)
			return errors.New("injected later failure")
		}
		return nil
	}, beforeRemove: nil})
	if err == nil {
		t.Fatal("expected failure")
	}
	got, _ := os.ReadFile(a)
	if string(got) != string(edit) {
		t.Fatalf("BUG: rollback discarded concurrent edit, got %q", got)
	}
}
func TestAuditSymlinkRenameRace(t *testing.T) {
	base := testDir(t)
	root := filepath.Join(base, "target")
	escaped := filepath.Join(base, "outside")
	os.Mkdir(root, 0700)
	p := filepath.Join(root, "a.go")
	before := []byte("package p\nvar A = 1\n")
	after := []byte("package p\nvar A = 2\n")
	os.WriteFile(p, before, 0600)
	err := commit([]Change{{Path: p, Before: before, After: after, Mode: 0600, Exists: true}}, fileOps{beforeRename: func(src, dst string) error {
		if e := os.Rename(root, escaped); e != nil {
			return e
		}
		if e := os.Symlink(escaped, root); e != nil {
			return e
		}
		return nil
	}, beforeRemove: nil})
	if err == nil {
		got, _ := os.ReadFile(filepath.Join(escaped, "a.go"))
		t.Fatalf("BUG: commit succeeded after parent symlink swap; outside file=%q", got)
	}
}
func TestAuditGeneratedPermissions(t *testing.T) {
	root := testDir(t)
	p := filepath.Join(root, "a.go")
	os.WriteFile(p, []byte("package p\nvar A = \"private example\"\n"), 0600)
	r, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = Commit(changes); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(root, GeneratedName))
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("BUG: private source strings moved into mode %04o file", info.Mode().Perm())
	}
}

func TestRecoveryRetainsLateOpenDescriptorEdits(t *testing.T) {
	root := testDir(t)
	a := writeFixture(t, root, "a.go", "old a")
	b := writeFixture(t, root, "b.go", "old b")
	changes := []Change{{Path: a, Before: []byte("old a"), After: []byte("new a"), Mode: 0644, Exists: true}, {Path: b, Before: []byte("old b"), After: []byte("new b"), Mode: 0644, Exists: true}}
	var editor *os.File
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst == b {
			var e error
			editor, e = os.OpenFile(a, os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			return errors.New("later failure")
		}
		return nil
	}})
	if err == nil || editor == nil {
		t.Fatal("missing rollback")
	}
	defer editor.Close()
	if _, e := editor.WriteAt([]byte("late!"), 0); e != nil {
		t.Fatal(e)
	}
	found := false
	for name, data := range treeSnapshot(t, root) {
		if strings.HasPrefix(name, ".strsweep-recovery-") {
			if data == "late!" && strings.Contains(err.Error(), name) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("late edit through open fd lost or recovery path omitted")
	}
	if string(readFixture(t, root, "a.go")) != "old a" {
		t.Fatal("original not restored")
	}
}
func TestRecoveryDoesNotOverwriteNewDestination(t *testing.T) {
	root := testDir(t)
	a := writeFixture(t, root, "a.go", "old a")
	b := writeFixture(t, root, "b.go", "old b")
	changes := []Change{{Path: a, Before: []byte("old a"), After: []byte("new a"), Mode: 0644, Exists: true}, {Path: b, Before: []byte("old b"), After: []byte("new b"), Mode: 0644, Exists: true}}
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst == b {
			return errors.New("later failure")
		}
		return nil
	}, beforeLink: func(src, dst string) error { return os.WriteFile(dst, []byte("new editor file"), 0600) }})
	if err == nil || !strings.Contains(err.Error(), "recovery failed") {
		t.Fatalf("missing conflict: %v", err)
	}
	if string(readFixture(t, root, "a.go")) != "new editor file" {
		t.Fatal("concurrent destination overwritten")
	}
}
func TestExistingGeneratedPermissionsNarrowWithoutContentChange(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package demo\nvar A = \"private\"\n")
	scanAndApply(t, root)
	generated := readFixture(t, root, GeneratedName)
	writeFixture(t, root, "b.go", "package demo\nvar B = \"private\"\n")
	if e := os.Chmod(filepath.Join(root, "b.go"), 0600); e != nil {
		t.Fatal(e)
	}
	scanAndApply(t, root)
	if string(readFixture(t, root, GeneratedName)) != string(generated) {
		t.Fatal("constant unexpectedly changed")
	}
	info, _ := os.Stat(filepath.Join(root, GeneratedName))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
func TestGeneratedPermissionsNeverAddReadOrWriteBits(t *testing.T) {
	for _, mode := range []fs.FileMode{0400, 0444, 0600, 0640, 0644} {
		t.Run(mode.String(), func(t *testing.T) {
			root := testDir(t)
			p := writeFixture(t, root, "a.go", "package demo\nvar A = \"candidate\"\n")
			os.Chmod(p, mode)
			scanAndApply(t, root)
			info, _ := os.Stat(filepath.Join(root, GeneratedName))
			if info.Mode().Perm() & ^mode.Perm() != 0 {
				t.Fatalf("source %o generated %o", mode.Perm(), info.Mode().Perm())
			}
			if mode&0004 == 0 && info.Mode().Perm()&0077 != 0 {
				t.Fatal("private value exposed")
			}
		})
	}
}

func TestPinnedRenameAfterFinalCheckNeverWritesOutside(t *testing.T) {
	base := testDir(t)
	root := filepath.Join(base, "target")
	outside := filepath.Join(base, "outside")
	parked := filepath.Join(base, "parked")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, root, "a.go", "before")
	writeFixture(t, outside, "a.go", "outside sentinel")
	err := commit([]Change{{Path: path, Before: []byte("before"), After: []byte("after"), Mode: 0644, Exists: true}}, fileOps{afterCheck: func(string) error {
		// Populate a distinct outside directory with the same staging names, so
		// an absolute-path rename would succeed and overwrite its sentinel.
		entries, e := os.ReadDir(root)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".strsweep-") {
				data, e := os.ReadFile(filepath.Join(root, entry.Name()))
				if e != nil {
					return e
				}
				if e = os.WriteFile(filepath.Join(outside, entry.Name()), data, 0600); e != nil {
					return e
				}
			}
		}
		if e = os.Rename(root, parked); e != nil {
			return e
		}
		return os.Symlink(outside, root)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(readFixture(t, outside, "a.go")); got != "outside sentinel" {
		t.Fatalf("outside target overwritten: %q", got)
	}
	if got := string(readFixture(t, parked, "a.go")); got != "after" {
		t.Fatalf("operation did not stay on original directory handle: %q", got)
	}
}
