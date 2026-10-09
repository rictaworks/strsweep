//go:build !windows && !js && !plan9

package core

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, name, source string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFixture(t *testing.T, root, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func findRow(t *testing.T, result *Result, path string) Row {
	t.Helper()
	for _, row := range result.Rows {
		if filepath.ToSlash(row.Path) == path {
			return *row
		}
	}
	t.Fatalf("no row for %q: %#v", path, result.Rows)
	return Row{}
}

func scanAndApply(t *testing.T, root string) *Result {
	t.Helper()
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := Commit(changes); err != nil {
		t.Fatal(err)
	}
	return result
}

func generatedValues(t *testing.T, root string) map[string]string {
	t.Helper()
	source := readFixture(t, root, "strsweep_const.go")
	f, err := parser.ParseFile(token.NewFileSet(), "strsweep_const.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Names) != 1 || len(value.Values) != 1 {
				t.Fatalf("unexpected generated declaration: %#v", value)
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatalf("generated constant is not a string literal: %#v", value.Values[0])
			}
			unquoted, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			name := value.Names[0].Name
			values[name] = unquoted
			names = append(names, name)
			if ast.IsExported(name) {
				t.Errorf("constant %s unexpectedly exported", name)
			}
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("generated constants not sorted: %v", names)
	}
	return values
}

func checkPackageTypes(t *testing.T, root string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(root, entry.Name()), nil, parser.AllErrors)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if _, err := new(types.Config).Check("example.test/demo", fset, files, nil); err != nil {
		t.Fatalf("rewritten package does not type-check: %v", err)
	}
}

func TestBaseName(t *testing.T) {
	for _, test := range []struct{ value, want string }{
		{"file not found", "strFileNotFound"},
		{"one two three four five", "strOneTwoThreeFour"},
		{"404 not found", "str404NotFound"},
		{"hello-world/foo_bar", "strHelloWorldFooBar"},
	} {
		t.Run(test.value, func(t *testing.T) {
			if got := BaseName(test.value); got != test.want {
				t.Errorf("BaseName(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
	pattern := regexp.MustCompile(`^strLit[0-9a-f]{6}$`)
	for _, value := range []string{"日本語", "!!!", "\n\t"} {
		name := BaseName(value)
		if !pattern.MatchString(name) || name != BaseName(value) {
			t.Errorf("invalid or unstable fallback name for %q: %q", value, name)
		}
	}
}

func TestScanUsesASTExclusionsAndDoesNotWrite(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "main.go", "package demo\nimport \"fmt\"\nconst already = \"existing\"\ntype payload struct { Name string `json:\"name\"` }\n// \"not a literal\"\nfunc run() string { fmt.Println(\"hello world\", `hello world`); _ = \"\"; return \"say \\\"hi\\\"\" }\n")
	before := treeSnapshot(t, root)
	var paths []string
	result, err := Scan(root, func(done, total int, path string) {
		if done != len(paths)+1 || total != 1 {
			t.Errorf("unexpected progress: [%d/%d] %s", done, total, path)
		}
		paths = append(paths, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, result, "main.go")
	if row.Detected != 7 || row.Excluded != 4 || row.Target != 3 {
		t.Fatalf("unexpected literal counts: %#v", row)
	}
	if len(paths) != 1 {
		t.Errorf("progress called %d times, want 1", len(paths))
	}
	changes, err := Plan(result)
	if err != nil || len(changes) != 2 {
		t.Fatalf("Plan() = %d changes, %v; want source and constants", len(changes), err)
	}
	if after := treeSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("Scan or Plan modified the input tree")
	}
}

func TestScanSkipsExcludedFilesAndDirectories(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "z.go", "package demo\nvar Z = \"last\"\n")
	writeFixture(t, root, "a.go", "package demo\nvar A = \"first\"\n")
	writeFixture(t, root, "unit_test.go", "package demo_test\nvar want = \"expected\"\n")
	writeFixture(t, root, "generated.go", "// Code generated by a fixture. DO NOT EDIT.\npackage demo\nvar Auto = \"generated\"\n")
	for _, name := range []string{"vendor/bad.go", "testdata/bad.go", ".git/bad.go", ".hidden/bad.go"} {
		writeFixture(t, root, name, "not valid Go at all")
	}
	before := treeSnapshot(t, root)
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, row := range result.Rows {
		paths = append(paths, filepath.ToSlash(row.Path))
		if strings.Contains(row.Path, "/") {
			t.Errorf("excluded directory appeared in rows: %s", row.Path)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("rows not lexically sorted: %v", paths)
	}
	for _, name := range []string{"unit_test.go", "generated.go"} {
		row := findRow(t, result, name)
		if row.Target != 0 || !strings.Contains(row.Status, "スキップ") {
			t.Errorf("excluded file was not skipped: %#v", row)
		}
	}
	changes, err := Plan(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := Commit(changes); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unit_test.go", "generated.go", "vendor/bad.go", "testdata/bad.go", ".git/bad.go", ".hidden/bad.go"} {
		if got := string(readFixture(t, root, name)); got != before[name] {
			t.Errorf("excluded file %s changed", name)
		}
	}
}

func TestApplyDeduplicatesValuesAndIsIdempotent(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package demo\nvar A = \"hello world\"\n")
	writeFixture(t, root, "b.go", "package demo\nvar B = `hello world`\nvar C = \"hello\\x20world\"\nvar D = \"日本語\"\n")
	scanAndApply(t, root)
	values := generatedValues(t, root)
	if len(values) != 2 || values["strHelloWorld"] != "hello world" {
		t.Fatalf("equivalent strings were not aggregated: %v", values)
	}
	checkPackageTypes(t, root)
	before := treeSnapshot(t, root)
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Rows {
		if row.Target != 0 {
			t.Errorf("second scan has targets: %#v", row)
		}
	}
	changes, err := Plan(result)
	if err != nil || len(changes) != 0 {
		t.Fatalf("second Plan = %d changes, %v", len(changes), err)
	}
	if err := Commit(changes); err != nil {
		t.Fatal(err)
	}
	if after := treeSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("second apply changed files")
	}
}

func TestApplyAvoidsLocalAndPackageIdentifiers(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package demo\nvar strHelloWorld2 = 2\nfunc f() string { strHelloWorld := 1; _ = strHelloWorld; return \"hello world\" }\n")
	scanAndApply(t, root)
	values := generatedValues(t, root)
	if values["strHelloWorld3"] != "hello world" {
		t.Fatalf("name collides with an existing identifier: %v", values)
	}
	checkPackageTypes(t, root)
}

func TestNamingDoesNotDependOnFileOrder(t *testing.T) {
	var generated []byte
	for _, reverse := range []bool{false, true} {
		root := testDir(t)
		first, second := "hello-world", "hello world"
		if reverse {
			first, second = second, first
		}
		writeFixture(t, root, "a.go", "package demo\nvar A = "+strconv.Quote(first)+"\n")
		writeFixture(t, root, "z.go", "package demo\nvar Z = "+strconv.Quote(second)+"\n")
		scanAndApply(t, root)
		current := readFixture(t, root, "strsweep_const.go")
		if generated != nil && !bytes.Equal(generated, current) {
			t.Fatalf("naming depends on traversal order:\n%s\nversus\n%s", generated, current)
		}
		generated = current
		checkPackageTypes(t, root)
	}
}

func TestSeparatePackagesGetSeparateConstantFiles(t *testing.T) {
	root := testDir(t)
	for _, dir := range []string{"first", "second"} {
		writeFixture(t, root, dir+"/source.go", "package "+dir+"\nvar V = \"same value\"\n")
	}
	scanAndApply(t, root)
	for _, dir := range []string{"first", "second"} {
		values := generatedValues(t, filepath.Join(root, dir))
		if len(values) != 1 || values["strSameValue"] != "same value" {
			t.Errorf("unexpected constants for %s: %v", dir, values)
		}
		checkPackageTypes(t, filepath.Join(root, dir))
	}
}

func TestMixedPackagesAreSkipped(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package first\nvar A = \"first\"\n")
	writeFixture(t, root, "b.go", "package second\nvar B = \"second\"\n")
	before := treeSnapshot(t, root)
	result := scanAndApply(t, root)
	for _, row := range result.Rows {
		if row.Target != 0 || !strings.Contains(row.Status, "スキップ") {
			t.Errorf("mixed package file not skipped: %#v", row)
		}
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("mixed package directory changed")
	}
}

func TestGeneratedDestinationCollisionDoesNotOverwriteUserFile(t *testing.T) {
	for _, destination := range []string{
		"package demo\nvar Existing = 1\n",
		"// Code generated by someone else. DO NOT EDIT.\npackage demo\nconst Existing = \"keep\"\n",
		"package demo\n// Code generated by strsweep. DO NOT EDIT.\nvar Existing = 1\n",
	} {
		t.Run(strconv.Quote(destination), func(t *testing.T) {
			root := testDir(t)
			writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
			writeFixture(t, root, "strsweep_const.go", destination)
			before := treeSnapshot(t, root)
			result := scanAndApply(t, root)
			row := findRow(t, result, "source.go")
			if row.Target != 0 || !strings.Contains(row.Status, "スキップ") {
				t.Errorf("package with destination collision was not skipped: %#v", row)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Fatal("destination collision changed user files")
			}
		})
	}
}

func TestNewLiteralsReuseAndPreserveOldGeneratedConstants(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package demo\nvar A = \"original\"\n")
	scanAndApply(t, root)
	writeFixture(t, root, "b.go", "package demo\nvar B = \"original\"\nvar C = \"new value\"\n")
	scanAndApply(t, root)
	values := generatedValues(t, root)
	if len(values) != 2 || values["strOriginal"] != "original" || values["strNewValue"] != "new value" {
		t.Fatalf("existing generated constants lost, duplicated, or renamed: %v", values)
	}
	checkPackageTypes(t, root)
}

func TestApplyPreservesLineEndingsAndFinalNewline(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, finalNewline := range []bool{false, true} {
			t.Run(strconv.Quote(newline)+"/final="+strconv.FormatBool(finalNewline), func(t *testing.T) {
				root := testDir(t)
				source := "package demo\n\n// keep this spacing\nvar A    = `first\nsecond` // after raw literal\nvar B = \"quoted\""
				if finalNewline {
					source += "\n"
				}
				source = strings.ReplaceAll(source, "\n", newline)
				writeFixture(t, root, "source.go", source)
				scanAndApply(t, root)
				after := readFixture(t, root, "source.go")
				want := strings.Replace(source, "`first"+newline+"second`", "strFirstSecond", 1)
				want = strings.Replace(want, `"quoted"`, "strQuoted", 1)
				if string(after) != want {
					t.Fatalf("source formatting or raw literal boundary changed:\n got %q\nwant %q", after, want)
				}
				if got := bytes.HasSuffix(after, []byte("\n")); got != finalNewline {
					t.Errorf("trailing newline = %v, want %v", got, finalNewline)
				}
				if got := generatedValues(t, root)["strFirstSecond"]; got != "first\nsecond" {
					t.Errorf("raw string semantics changed: %q", got)
				}
				checkPackageTypes(t, root)
			})
		}
	}
}

func TestParseFailurePreventsAllChanges(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a_valid.go", "package demo\nvar A = \"candidate\"\n")
	writeFixture(t, root, "b_invalid.go", "package demo\nfunc broken( {\n")
	writeFixture(t, root, "c_later.go", "package demo\nvar C = \"later\"\n")
	before := treeSnapshot(t, root)
	result, err := Scan(root, nil)
	if err == nil {
		t.Fatal("Scan accepted an invalid Go source file")
	}
	if result == nil {
		t.Fatal("Scan must retain rows to render the parse error")
	}
	row := findRow(t, result, "b_invalid.go")
	if !strings.Contains(row.Status, "エラー") {
		t.Errorf("parse failure row has no error status: %#v", row)
	}
	if changes, err := Plan(result); err == nil || len(changes) != 0 {
		t.Fatalf("Plan accepted a failed scan: %d changes, error %v", len(changes), err)
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("failed scan/plan changed files")
	}
}

func TestScanDoesNotFollowSymlinks(t *testing.T) {
	root, outside := testDir(t), testDir(t)
	writeFixture(t, outside, "source.go", "package demo\nvar Secret = \"outside\"\n")
	writeFixture(t, root, "source.go", "package demo\nvar A = \"inside\"\n")
	for _, link := range []struct{ target, name string }{
		{outside, "linked"},
		{filepath.Join(outside, "source.go"), "linked.go"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	before := treeSnapshot(t, outside)
	result := scanAndApply(t, root)
	for _, row := range result.Rows {
		if strings.HasPrefix(row.Path, "linked") && row.Target != 0 {
			t.Errorf("symlink treated as writable source: %#v", row)
		}
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, outside)) {
		t.Fatal("apply wrote outside the target root through a symlink")
	}
}

func TestCommitPreservesPermissions(t *testing.T) {
	root := testDir(t)
	path := writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	scanAndApply(t, root)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Errorf("source permissions changed to %o, want 640", got)
	}
}

func TestCommitFailureLeavesOriginalFilesIntact(t *testing.T) {
	root := testDir(t)
	path := writeFixture(t, root, "a.go", "package demo\nvar A = \"original\"\n")
	blocked := filepath.Join(root, "blocked.go")
	if err := os.Mkdir(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "blocked.go/keep", "must survive")
	before := treeSnapshot(t, root)
	changes := []Change{
		{Path: path, Before: []byte(before["a.go"]), After: []byte("package demo\nvar A = \"changed\"\n"), Mode: 0644, Exists: true},
		{Path: blocked, After: []byte("package demo\n"), Mode: 0644, Exists: false},
	}
	if err := Commit(changes); err == nil {
		t.Fatal("Commit unexpectedly replaced a directory")
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("failed commit altered files or left temporary files")
	}
}

func TestCommitRejectsSourceChangesAfterPlanning(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(result)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "source.go", "package demo\nvar A = \"user edit\"\n")
	before := treeSnapshot(t, root)
	if err := Commit(changes); err == nil {
		t.Fatal("Commit overwrote a concurrent user edit")
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("failed stale commit modified files")
	}
}

func TestCommitRejectsDestinationCreatedAfterPlanning(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(result)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "strsweep_const.go", "package demo\nconst UserCreated = 42\n")
	before := treeSnapshot(t, root)
	if err := Commit(changes); err == nil {
		t.Fatal("Commit overwrote a destination created after planning")
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("failed commit modified source or concurrent destination")
	}
}

func TestGeneratedMarkerInsideRawStringIsNotAComment(t *testing.T) {
	root := testDir(t)
	source := "package demo\nvar A = `before\n// Code generated by another program. DO NOT EDIT.\nafter`\n"
	writeFixture(t, root, "source.go", source)
	result := scanAndApply(t, root)
	row := findRow(t, result, "source.go")
	if row.Target != 1 {
		t.Fatalf("text inside a raw literal was mistaken for a generated-code comment: %#v", row)
	}
	checkPackageTypes(t, root)
}

func TestAllLiteralExpressionsInsideConstAreExcluded(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "source.go", "package demo\nconst (\n A = \"first\" + `second`\n B = len(\"third\")\n)\nvar C = \"candidate\"\n")
	result := scanAndApply(t, root)
	row := findRow(t, result, "source.go")
	if row.Detected != 4 || row.Excluded != 3 || row.Target != 1 {
		t.Fatalf("const subtree exclusions incorrect: %#v", row)
	}
	if values := generatedValues(t, root); len(values) != 1 || values["strCandidate"] != "candidate" {
		t.Errorf("unexpected generated constants: %v", values)
	}
	checkPackageTypes(t, root)
}

func TestEmptyTreeAndInvalidRoots(t *testing.T) {
	root := testDir(t)
	result, err := Scan(root, nil)
	if err != nil || len(result.Rows) != 0 {
		t.Fatalf("empty tree: %#v, %v", result, err)
	}
	changes, err := Plan(result)
	if err != nil || len(changes) != 0 {
		t.Fatalf("empty plan: %#v, %v", changes, err)
	}
	file := writeFixture(t, root, "file.go", "package demo\n")
	for _, invalid := range []string{file, filepath.Join(root, "missing")} {
		if _, err := Scan(invalid, nil); err == nil {
			t.Errorf("Scan accepted invalid root %s", invalid)
		}
	}
}

func TestPlanValidatesEverythingBeforeWriting(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package demo\nvar A = \"first\"\n")
	writeFixture(t, root, "b.go", "package demo\nvar B = \"second\"\n")
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, root)
	// Corrupt the in-memory plan so that a later file fails re-parsing.
	delete(result.packages[0].values, "second")
	if changes, err := Plan(result); err == nil || len(changes) != 0 {
		t.Fatalf("Plan returned an unvalidated partial change set: %d changes, %v", len(changes), err)
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Fatal("validation failure modified files")
	}
}

func TestCommitRejectsSymlinkIntroducedAfterPlanning(t *testing.T) {
	root, outside := testDir(t), testDir(t)
	path := writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
	outsidePath := writeFixture(t, outside, "outside.go", "package outside\nvar V = \"untouched\"\n")
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := treeSnapshot(t, outside)
	if err := Commit(changes); err == nil {
		t.Fatal("Commit accepted a new symlink source")
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, outside)) {
		t.Fatal("Commit wrote through a new source symlink")
	}
	if _, err := os.Lstat(filepath.Join(root, GeneratedName)); !os.IsNotExist(err) {
		t.Fatalf("failed commit left a generated file: %v", err)
	}
}

func TestCommitRejectsSymlinkParentIntroducedAfterPlanning(t *testing.T) {
	root, outside := testDir(t), testDir(t)
	writeFixture(t, root, "pkg/source.go", "package demo\nvar A = \"candidate\"\n")
	writeFixture(t, outside, "source.go", "package demo\nvar A = \"candidate\"\n")
	result, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "pkg"), filepath.Join(root, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "pkg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := treeSnapshot(t, outside)
	if err := Commit(changes); err == nil {
		t.Fatal("Commit accepted a newly symlinked parent directory")
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, outside)) {
		t.Fatal("Commit wrote through a new parent symlink")
	}
}

func TestCommitRollsBackAfterRenameFailure(t *testing.T) {
	root := testDir(t)
	first := writeFixture(t, root, "a.go", "before a\n")
	second := writeFixture(t, root, "b.go", "before b\n")
	before := treeSnapshot(t, root)
	changes := []Change{
		{Path: first, Before: []byte("before a\n"), After: []byte("after a\n"), Mode: 0644, Exists: true},
		{Path: second, Before: []byte("before b\n"), After: []byte("after b\n"), Mode: 0644, Exists: true},
	}
	injected := errors.New("injected second rename failure")
	calls := 0
	err := commit(changes, fileOps{
		beforeRename: func(old, new string) error {
			calls++
			if calls == 2 {
				return injected
			}
			return nil
		},
		beforeRemove: nil,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Commit error = %v, want injected failure", err)
	}
	if calls != 3 {
		t.Errorf("rename calls = %d, want 2 replacements + 1 rollback", calls)
	}
	if !reflect.DeepEqual(before, withoutRecovery(treeSnapshot(t, root))) {
		t.Fatal("rollback failed to restore originals or clean temporary files")
	}
}

func TestCommitRollbackRemovesNewlyCreatedFiles(t *testing.T) {
	root := testDir(t)
	existing := writeFixture(t, root, "z.go", "before\n")
	before := treeSnapshot(t, root)
	newPath := filepath.Join(root, "a.go")
	changes := []Change{
		{Path: newPath, After: []byte("new file\n"), Mode: 0644},
		{Path: existing, Before: []byte("before\n"), After: []byte("after\n"), Mode: 0644, Exists: true},
	}
	injected := errors.New("injected existing-file replacement failure")
	err := commit(changes, fileOps{
		beforeRename: func(old, new string) error {
			if new == existing {
				return injected
			}
			return nil
		},
		beforeRemove: nil,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Commit error = %v, want injected failure", err)
	}
	if !reflect.DeepEqual(before, withoutRecovery(treeSnapshot(t, root))) {
		t.Fatal("rollback left a newly created file or changed the original")
	}
}

func TestCommitRetainsRecoveryBackupWhenRollbackFails(t *testing.T) {
	root := testDir(t)
	first := writeFixture(t, root, "a.go", "before a\n")
	second := writeFixture(t, root, "b.go", "before b\n")
	changes := []Change{
		{Path: first, Before: []byte("before a\n"), After: []byte("after a\n"), Mode: 0644, Exists: true},
		{Path: second, Before: []byte("before b\n"), After: []byte("after b\n"), Mode: 0644, Exists: true},
	}
	calls := 0
	err := commit(changes, fileOps{
		beforeRename: func(old, new string) error {
			calls++
			if calls >= 2 {
				return errors.New("injected rename and recovery failure")
			}
			return nil
		},
		beforeRemove: nil,
	})
	if err == nil || !strings.Contains(err.Error(), "backup") || !strings.Contains(err.Error(), first) {
		t.Fatalf("failure does not explain recovery location: %v", err)
	}
	foundBackup := false
	for name, content := range treeSnapshot(t, root) {
		if strings.HasPrefix(name, ".strsweep-") && content == "before a\n" {
			foundBackup = true
			if !strings.Contains(err.Error(), filepath.Join(root, name)) {
				t.Errorf("retained backup absent from error: %s", name)
			}
		}
	}
	if !foundBackup {
		t.Fatal("rollback failure destroyed the only recoverable original")
	}
	if got := string(readFixture(t, root, "b.go")); got != "before b\n" {
		t.Fatalf("unapplied source changed: %q", got)
	}
}

func TestCommitReportsFailureRemovingNewFileDuringRollback(t *testing.T) {
	root := testDir(t)
	existing := writeFixture(t, root, "z.go", "before\n")
	newPath := filepath.Join(root, "a.go")
	changes := []Change{
		{Path: newPath, After: []byte("new file\n"), Mode: 0644},
		{Path: existing, Before: []byte("before\n"), After: []byte("after\n"), Mode: 0644, Exists: true},
	}
	err := commit(changes, fileOps{
		beforeRename: func(old, new string) error {
			if new == existing {
				return errors.New("injected rename failure")
			}
			return nil
		},
		beforeRemove: func(path string) error { return errors.New("injected cleanup failure") },
	})
	if err == nil || !strings.Contains(err.Error(), "recovery failed") || !strings.Contains(err.Error(), newPath) {
		t.Fatalf("new-file rollback failure not reported clearly: %v", err)
	}
	if got := string(readFixture(t, root, "z.go")); got != "before\n" {
		t.Fatalf("original changed: %q", got)
	}
}

func TestExistingConstantShadowingStopsWithoutWrites(t *testing.T) {
	for _, source := range []string{
		"package demo\nfunc f() string { strOriginal := 42; _ = strOriginal; return \"original\" }\n",
		"package demo\nimport strOriginal \"example.test/provider\"\nfunc f() string { _ = strOriginal.Label; return \"original\" }\n",
		"package demo\nimport \"example.test/strOriginal\"\nfunc f() string { _ = strOriginal.Label; return \"original\" }\n",
	} {
		t.Run(strconv.Quote(source), func(t *testing.T) {
			root := testDir(t)
			writeFixture(t, root, "a.go", "package demo\nvar A = \"original\"\n")
			scanAndApply(t, root)
			writeFixture(t, root, "b.go", source)
			before := treeSnapshot(t, root)
			result, err := Scan(root, nil)
			if err == nil {
				t.Fatal("Scan allowed reusing an existing constant shadowed by a local or imported identifier")
			}
			if changes, err := Plan(result); err == nil || len(changes) != 0 {
				t.Fatalf("Plan accepted a shadowed constant: %d changes, %v", len(changes), err)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Fatal("shadowing error modified files")
			}
		})
	}
}

func TestMalformedOwnedGeneratedFilesAreRejected(t *testing.T) {
	for _, body := range []string{
		"package demo\nfunc broken( {\n",
		"package demo\nvar strOriginal = \"original\"\n",
		"package demo\nconst strOriginal string = \"original\"\n",
		"package demo\nconst (strOriginal = \"original\"; strDuplicate = \"original\")\n",
		"package demo\nconst (strOriginal = \"original\"; strOriginal = \"other\")\n",
		"package demo\nconst UnownedName = \"original\"\n",
		"package demo\nconst strOriginal = 42\n",
		"package demo\nconst strOriginal = \"first\" + \"second\"\n",
		"package demo\nconst strA, strB = \"first\", \"second\"\n",
	} {
		t.Run(strconv.Quote(body), func(t *testing.T) {
			root := testDir(t)
			writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
			writeFixture(t, root, GeneratedName, Marker+"\n"+body)
			before := treeSnapshot(t, root)
			result, err := Scan(root, nil)
			if err == nil {
				t.Fatal("Scan accepted malformed or manually altered owned generated constants")
			}
			if changes, err := Plan(result); err == nil || len(changes) != 0 {
				t.Fatalf("Plan accepted malformed generated file: %d changes, %v", len(changes), err)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Fatal("malformed generated file was modified")
			}
		})
	}
}

func TestUpdatedGeneratedFilePreservesLineEndings(t *testing.T) {
	root := testDir(t)
	writeFixture(t, root, "a.go", "package demo\nvar A = \"original\"\n")
	scanAndApply(t, root)
	generated := string(readFixture(t, root, GeneratedName))
	generated = strings.ReplaceAll(strings.TrimSuffix(generated, "\n"), "\n", "\r\n")
	writeFixture(t, root, GeneratedName, generated)
	writeFixture(t, root, "b.go", "package demo\nvar B = \"new value\"\n")
	scanAndApply(t, root)
	after := readFixture(t, root, GeneratedName)
	if bytes.HasSuffix(after, []byte("\n")) {
		t.Error("added a final newline to existing generated file")
	}
	if bytes.Contains(bytes.ReplaceAll(after, []byte("\r\n"), nil), []byte("\n")) {
		t.Error("replaced CRLF with lone LF in existing generated file")
	}
	if values := generatedValues(t, root); len(values) != 2 {
		t.Errorf("lost generated constants during update: %v", values)
	}
	checkPackageTypes(t, root)
}

func TestSymlinkAndDirectoryGeneratedDestinationsAreSkipped(t *testing.T) {
	for _, asSymlink := range []bool{false, true} {
		t.Run("symlink="+strconv.FormatBool(asSymlink), func(t *testing.T) {
			root := testDir(t)
			writeFixture(t, root, "source.go", "package demo\nvar A = \"candidate\"\n")
			destination := filepath.Join(root, GeneratedName)
			if asSymlink {
				outside := testDir(t)
				path := writeFixture(t, outside, "constants.go", "package demo\nconst Keep = 42\n")
				if err := os.Symlink(path, destination); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else if err := os.Mkdir(destination, 0755); err != nil {
				t.Fatal(err)
			}
			before := treeSnapshot(t, root)
			result := scanAndApply(t, root)
			if row := findRow(t, result, "source.go"); row.Target != 0 || !strings.Contains(row.Status, "スキップ") {
				t.Errorf("nonregular destination was not reported as a collision: %#v", row)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Fatal("nonregular destination collision changed files")
			}
		})
	}
}

func withoutRecovery(snapshot map[string]string) map[string]string {
	for name := range snapshot {
		if strings.HasPrefix(name, ".strsweep-recovery-") {
			delete(snapshot, name)
		}
	}
	return snapshot
}
