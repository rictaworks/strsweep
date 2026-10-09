package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditConcurrentRollback(t *testing.T) {
	d := t.TempDir()
	a := filepath.Join(d, "a.go")
	b := filepath.Join(d, "b.go")
	before := []byte("package p\nvar A = 1\n")
	after := []byte("package p\nvar A = 2\n")
	edit := []byte("package p\nvar A = 3 // editor update\n")
	os.WriteFile(a, before, 0600)
	os.WriteFile(b, before, 0600)
	n := 0
	err := commit([]Change{{a, before, after, 0600, true}, {b, before, after, 0600, true}}, fileOps{rename: func(src, dst string) error {
		n++
		if n == 2 {
			os.WriteFile(a, edit, 0600)
			return errors.New("injected later failure")
		}
		return os.Rename(src, dst)
	}, remove: os.Remove})
	if err == nil {
		t.Fatal("expected failure")
	}
	got, _ := os.ReadFile(a)
	if string(got) != string(edit) {
		t.Fatalf("BUG: rollback discarded concurrent edit, got %q", got)
	}
}
func TestAuditSymlinkRenameRace(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "target")
	escaped := filepath.Join(base, "outside")
	os.Mkdir(root, 0700)
	p := filepath.Join(root, "a.go")
	before := []byte("package p\nvar A = 1\n")
	after := []byte("package p\nvar A = 2\n")
	os.WriteFile(p, before, 0600)
	err := commit([]Change{{p, before, after, 0600, true}}, fileOps{rename: func(src, dst string) error {
		if e := os.Rename(root, escaped); e != nil {
			return e
		}
		if e := os.Symlink(escaped, root); e != nil {
			return e
		}
		return os.Rename(src, dst)
	}, remove: os.Remove})
	if err == nil {
		got, _ := os.ReadFile(filepath.Join(escaped, "a.go"))
		t.Fatalf("BUG: commit succeeded after parent symlink swap; outside file=%q", got)
	}
}
func TestAuditGeneratedPermissions(t *testing.T) {
	root := t.TempDir()
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
