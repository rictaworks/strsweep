//go:build linux

package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReviewCommitRejectsIdenticalSourceReplacement(t *testing.T) {
	root := testDir(t)
	path := writeFixture(t, root, "a.go", "package demo\nvar A = \"candidate\"\n")
	r, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, root)
	if err := Commit(changes); err == nil {
		t.Fatal("accepted identical bytes in a different source inode")
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("stale commit altered source replacement")
	}
}

func TestReviewCommitRejectsIdenticalDirectoryReplacement(t *testing.T) {
	for _, replaceRoot := range []bool{true, false} {
		name := "package"
		if replaceRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			base := testDir(t)
			root := filepath.Join(base, "root")
			data := "package demo\nvar A = \"candidate\"\n"
			writeFixture(t, root, "pkg/a.go", data)
			r, err := Scan(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			changes, err := Plan(r)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "pkg")
			if replaceRoot {
				dir = root
			}
			if err := os.Rename(dir, dir+".saved"); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, root, "pkg/a.go", data)
			before := treeSnapshot(t, base)
			if err := Commit(changes); err == nil {
				t.Fatal("accepted replacement directory")
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, base)) {
				t.Fatal("stale commit altered replacement directory")
			}
		})
	}
}

func TestReviewRollbackPreservesEditedNewFile(t *testing.T) {
	root := testDir(t)
	a := filepath.Join(root, "a.go")
	b := writeFixture(t, root, "b.go", "before b")
	changes := []Change{{Path: a, After: []byte("new a"), Mode: 0644}, {Path: b, Before: []byte("before b"), After: []byte("after b"), Mode: 0644, Exists: true}}
	injected := errors.New("later replacement failure")
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst == b {
			if err := os.WriteFile(a, []byte("editor content"), 0644); err != nil {
				t.Fatal(err)
			}
			return injected
		}
		return nil
	}})
	if !errors.Is(err, injected) {
		t.Fatalf("unexpected failure: %v", err)
	}
	if got := string(readFixture(t, root, "a.go")); got != "editor content" {
		t.Fatalf("new-file edit lost: %q", got)
	}
	if got := string(readFixture(t, root, "b.go")); got != "before b" {
		t.Fatalf("untouched original changed: %q", got)
	}
}

func TestReviewRollbackReportsRetainedOriginalBackups(t *testing.T) {
	root := testDir(t)
	a := writeFixture(t, root, "a.go", "before a")
	b := writeFixture(t, root, "b.go", "before b")
	changes := []Change{{Path: a, Before: []byte("before a"), After: []byte("after a"), Mode: 0644, Exists: true}, {Path: b, Before: []byte("before b"), After: []byte("after b"), Mode: 0644, Exists: true}}
	err := commit(changes, fileOps{beforeRename: func(src, dst string) error {
		if dst == b {
			if err := os.WriteFile(a, []byte("editor content"), 0644); err != nil {
				t.Fatal(err)
			}
			return errors.New("later replacement failure")
		}
		return nil
	}})
	if err == nil {
		t.Fatal("expected failure")
	}
	found := false
	for name, content := range treeSnapshot(t, root) {
		if strings.HasPrefix(name, ".strsweep-") && !strings.HasPrefix(name, ".strsweep-recovery-") {
			if content == "before a" {
				found = true
			}
			if !strings.Contains(err.Error(), filepath.Join(root, name)) {
				t.Errorf("retained backup %s containing %q is not reported: %v", name, content, err)
			}
		}
	}
	if !found {
		t.Fatal("original content backup was not retained")
	}
}

func TestReviewBootstrapRejectsRootAndAncestorSymlinks(t *testing.T) {
	for _, relative := range []bool{false, true} {
		for _, ancestor := range []bool{false, true} {
			name := "root-absolute"
			if ancestor {
				name = "ancestor-absolute"
			}
			if relative {
				name += "-relative"
			}
			t.Run(name, func(t *testing.T) {
				base := testDir(t)
				real := filepath.Join(base, "real")
				writeFixture(t, real, "nested/a.go", "package demo\nvar A = \"outside\"\n")
				target := filepath.Join(real, "nested")
				if ancestor {
					target = real
				}
				if relative {
					var err error
					target, err = filepath.Rel(base, target)
					if err != nil {
						t.Fatal(err)
					}
				}
				link := filepath.Join(base, "alias")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				root := link
				if ancestor {
					root = filepath.Join(root, "nested")
				}
				if result, err := Scan(root, nil); err == nil || result != nil {
					t.Fatalf("symlink bootstrap returned result=%v, err=%v", result, err)
				}
			})
		}
	}
}
