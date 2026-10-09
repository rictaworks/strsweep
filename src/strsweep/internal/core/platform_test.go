package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlatformSafetyBoundary(t *testing.T) {
	root := testDir(t)
	source := filepath.Join(root, "main.go")
	original := []byte("package demo\nvar A = \"example\"\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Scan(root, nil)
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		if err == nil {
			t.Fatal("unsafe pathname-backed platform accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 1 {
		t.Fatal("scan did not produce candidate")
	}
	changes, err := Plan(r)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		err = Commit(changes)
		if err == nil || !strings.Contains(err.Error(), "ACL") {
			t.Fatalf("Windows apply did not fail closed: %v", err)
		}
		got, e := os.ReadFile(source)
		if e != nil || string(got) != string(original) {
			t.Fatal("unsupported apply changed source")
		}
		if _, e = os.Stat(filepath.Join(root, GeneratedName)); !os.IsNotExist(e) {
			t.Fatal("unsupported apply created generated file")
		}
	}
}

func testDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
