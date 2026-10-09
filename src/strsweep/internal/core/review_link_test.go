//go:build linux

package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestFinalReviewRollbackUsesPrivateRecoveryDirectory(t *testing.T) {
	root := testDir(t)
	a := writeFixture(t, root, "a.go", "before a")
	b := writeFixture(t, root, "b.go", "before b")
	changes := []Change{{Path: a, Before: []byte("before a"), After: []byte("after a"), Mode: 0644, Exists: true}, {Path: b, Before: []byte("before b"), After: []byte("after b"), Mode: 0644, Exists: true}}
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst == b {
			return errors.New("later failure")
		}
		return nil
	}})
	if err == nil {
		t.Fatal("expected injected failure")
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	found := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".strsweep-recovery-") {
			continue
		}
		found = true
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("recovery container is not a private directory: %s %s", entry.Name(), info.Mode())
		}
		if !strings.Contains(err.Error(), filepath.Join(root, entry.Name())) {
			t.Fatalf("recovery directory is not reported: %s", entry.Name())
		}
	}
	if !found {
		t.Fatal("missing recovery directory")
	}
}

func TestFinalReviewRollbackRejectsRecoveryDirectorySymlinkSwap(t *testing.T) {
	root := testDir(t)
	a := writeFixture(t, root, "a.go", "before a")
	b := writeFixture(t, root, "b.go", "before b")
	writeFixture(t, root, "public/snapshot", "public sentinel")
	changes := []Change{{Path: a, Before: []byte("before a"), After: []byte("after a"), Mode: 0644, Exists: true}, {Path: b, Before: []byte("before b"), After: []byte("after b"), Mode: 0644, Exists: true}}
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst == b {
			return errors.New("later failure")
		}
		if src == a && filepath.Base(dst) == "snapshot" {
			recoveryDir := filepath.Dir(dst)
			if err := os.Rename(recoveryDir, recoveryDir+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("public", recoveryDir); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}})
	if err == nil {
		t.Fatal("expected injected failure")
	}
	if got := string(readFixture(t, root, "public/snapshot")); got != "public sentinel" {
		t.Fatalf("recovery followed replaced container, overwriting public file: %q", got)
	}
}
