package core

import (
	"bytes"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Change struct {
	Path              string
	Before, After     []byte
	Mode              fs.FileMode
	Exists            bool
	root              string
	rootInfo, dirInfo fs.FileInfo
	sourceInfo        fs.FileInfo
	beforeMode        fs.FileMode
}

// Plan prepares and reparses every changed file before any disk writes occur.
func Plan(r *Result) ([]Change, error) {
	for _, row := range r.Rows {
		if strings.HasPrefix(row.Status, "エラー") || row.Status == "未処理" {
			return nil, fmt.Errorf("scan did not finish successfully")
		}
	}
	var changes []Change
	for _, p := range r.packages {
		if len(p.counts) == 0 {
			continue
		}
		for _, s := range p.files {
			if len(s.literals) == 0 {
				continue
			}
			var out bytes.Buffer
			offset := 0
			for _, lit := range s.literals {
				out.Write(s.data[offset:lit.start])
				out.WriteString(p.values[lit.value])
				offset = lit.end
			}
			out.Write(s.data[offset:])
			changes = append(changes, Change{Path: s.path, Before: s.data, After: out.Bytes(), Mode: s.mode, Exists: true, root: r.Root, rootInfo: r.rootInfo, dirInfo: p.dirInfo, sourceInfo: s.info})
		}
		var out bytes.Buffer
		fmt.Fprintf(&out, "%s\n\npackage %s\n\nconst (\n", Marker, p.name)
		names := map[string]string{}
		var sorted []string
		for value, name := range p.values {
			names[name] = value
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			fmt.Fprintf(&out, "\t%s = %s\n", name, strconv.Quote(names[name]))
		}
		out.WriteString(")\n")
		data, err := format.Source(out.Bytes())
		if err != nil {
			return nil, err
		}
		c := Change{Path: filepath.Join(p.dir, GeneratedName), After: data, Mode: 0644, root: r.Root, rootInfo: r.rootInfo, dirInfo: p.dirInfo}
		if p.old != nil {
			c.Before = p.old.data
			c.sourceInfo = p.old.info
			c.beforeMode = p.old.mode
			c.Mode = p.old.mode
			c.Exists = true
			if bytes.Contains(c.Before, []byte("\r\n")) {
				c.After = bytes.ReplaceAll(c.After, []byte("\n"), []byte("\r\n"))
			}
			if !bytes.HasSuffix(c.Before, []byte("\n")) {
				c.After = bytes.TrimSuffix(c.After, []byte("\r\n"))
				c.After = bytes.TrimSuffix(c.After, []byte("\n"))
			}
		}
		// Intersect the contributor modes, then avoid relying on inherited
		// group ownership for any value that was not world-readable.
		c.Mode &= 0666
		private := c.Mode&0004 == 0
		for _, source := range p.files {
			if len(source.literals) > 0 {
				c.Mode &= source.mode.Perm()
				private = private || source.mode.Perm()&0004 == 0
			}
		}
		if private {
			c.Mode &= 0600
		}
		if !bytes.Equal(c.Before, c.After) || (p.old != nil && c.Mode != p.old.mode) {
			changes = append(changes, c)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	for _, c := range changes {
		if _, err := parser.ParseFile(token.NewFileSet(), c.Path, c.After, parser.AllErrors); err != nil {
			return nil, fmt.Errorf("rewrite validation %s: %w", c.Path, err)
		}
	}
	return changes, nil
}
