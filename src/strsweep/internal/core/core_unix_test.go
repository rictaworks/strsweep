//go:build linux

package core

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestScanDoesNotOpenNamedPipes(t *testing.T) {
	root := testDir(t)
	pipe := filepath.Join(root, "pipe.go")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Scan(root, nil)
	}()
	select {
	case <-done:
		// A skipped file or an immediate explicit error are both safe.
	case <-time.After(2 * time.Second):
		// Release a scanner blocked opening/reading the FIFO before cleanup.
		go func() {
			if file, err := os.OpenFile(pipe, os.O_WRONLY, 0); err == nil {
				_ = file.Close()
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("Scan blocked reading a named pipe rather than skipping nonregular files")
	}
}
