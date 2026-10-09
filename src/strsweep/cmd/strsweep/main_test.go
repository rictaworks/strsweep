package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"scan"}, {"apply", "."}, {"scan", ".", "--yes"}, {"apply", ".", "--yes", "--yes"}, {"scan", "a", "b"}, {"scan", "--bogus"}, {"scan", "/does-not-exist-strsweep"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, err bytes.Buffer
			if code := run(args, &out, &err); code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			if err.Len() == 0 {
				t.Fatal("missing error")
			}
		})
	}
}
func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"scan", "--help"}, {"apply", "--help"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 0 || !strings.Contains(out.String(), "終了コード") {
			t.Fatalf("code=%d out=%s err=%s", code, &out, &err)
		}
	}
}
func TestCLIWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := []byte("package example\nvar message = \"hello world\"\n")
	if e := os.WriteFile(path, original, 0644); e != nil {
		t.Fatal(e)
	}
	var out, err bytes.Buffer
	if code := run([]string{"scan", dir}, &out, &err); code != 0 {
		t.Fatalf("code=%d: %s", code, &err)
	}
	if !strings.Contains(out.String(), "strHelloWorld") || !strings.Contains(out.String(), "置換予定") || !strings.Contains(out.String(), "合計") {
		t.Fatalf("incomplete scan: %s", &out)
	}
	if err.Len() != 0 {
		t.Fatalf("nonterminal progress: %q", err.String())
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatal("scan wrote source")
	}
	out.Reset()
	err.Reset()
	if code := run([]string{"apply", dir}, &out, &err); code != 2 {
		t.Fatalf("missing yes code %d", code)
	}
	data, _ = os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatal("apply without yes wrote source")
	}
	out.Reset()
	err.Reset()
	if code := run([]string{"apply", dir, "--yes"}, &out, &err); code != 0 {
		t.Fatalf("apply code=%d: %s", code, &err)
	}
	if !strings.Contains(out.String(), "置換済") {
		t.Fatal(out.String())
	}
	first, _ := os.ReadFile(path)
	generated, _ := os.ReadFile(filepath.Join(dir, "strsweep_const.go"))
	out.Reset()
	err.Reset()
	if code := run([]string{"apply", "--yes", dir}, &out, &err); code != 0 {
		t.Fatalf("second apply code %d: %s", code, &err)
	}
	second, _ := os.ReadFile(path)
	gsecond, _ := os.ReadFile(filepath.Join(dir, "strsweep_const.go"))
	if !bytes.Equal(first, second) || !bytes.Equal(generated, gsecond) {
		t.Fatal("second apply changed files")
	}
}
func TestCLIParseError(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "bad.go"), []byte("package p\nvar ="), 0644); e != nil {
		t.Fatal(e)
	}
	var out, err bytes.Buffer
	if code := run([]string{"apply", dir, "--yes"}, &out, &err); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "エラー") || !strings.Contains(err.String(), "再実行") {
		t.Fatalf("out=%s err=%s", &out, &err)
	}
}
func TestNullIsNotTerminal(t *testing.T) {
	f, e := os.Open(os.DevNull)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("null device is not a terminal")
	}
}
