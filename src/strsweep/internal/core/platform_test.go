package core

import (
	"os"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS != "linux" {
		if err == nil {
			t.Fatal("unsupported platform accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 1 {
		t.Fatal("scan did not produce candidate")
	}
	if _, err := Plan(r); err != nil {
		t.Fatal(err)
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
