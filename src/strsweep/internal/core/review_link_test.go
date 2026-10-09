//go:build linux

package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalReviewRollbackMustNotChmodOutsideHardlink(t *testing.T) {
	base := testDir(t)
	root := filepath.Join(base, "root")
	a := writeFixture(t, root, "a.go", "before a")
	b := writeFixture(t, root, "b.go", "before b")
	outside := writeFixture(t, filepath.Join(base, "outside"), "file", "outside content")
	if err := os.Chmod(outside, 0644); err != nil {
		t.Fatal(err)
	}
	changes := []Change{{Path: a, Before: []byte("before a"), After: []byte("after a"), Mode: 0644, Exists: true}, {Path: b, Before: []byte("before b"), After: []byte("after b"), Mode: 0644, Exists: true}}
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst != b {
			return nil
		}
		if err := os.Remove(a); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(outside, a); err != nil {
			t.Fatal(err)
		}
		return errors.New("later failure")
	}})
	if err == nil {
		t.Fatal("expected injected failure")
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("rollback mutated outside hardlinked inode to mode %04o", info.Mode().Perm())
	}
}
