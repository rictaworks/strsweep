package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/rictaworks/strsweep/src/strsweep/internal/core"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

const usage = `strsweep: Go文字列リテラルをパッケージ内の非公開定数へ抽出します。
使い方:
  strsweep scan <dir>
  strsweep apply <dir> --yes
  strsweep --help
終了コード: 0 成功 / 1 解析・検証・読込失敗 / 2 引数エラー / 3 書込失敗
scanは書き込みません。applyは--yes必須。ネットワーク通信・対話入力なし。
`

func run(args []string, out, errout io.Writer) int {
	fail := func(code int, e error) int {
		fmt.Fprintf(errout, "strsweep: %v。原因を解消して再実行してください。\n", e)
		return code
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(out, usage)
		return 0
	}
	if len(args) == 0 || (args[0] != "scan" && args[0] != "apply") {
		fmt.Fprint(errout, usage)
		return fail(2, fmt.Errorf("scan または apply を指定してください"))
	}
	cmd := args[0]
	dir := ""
	yes := false
	for _, arg := range args[1:] {
		switch arg {
		case "--help", "-h":
			fmt.Fprint(out, usage)
			return 0
		case "--yes":
			if yes || cmd != "apply" {
				return fail(2, fmt.Errorf("無効な --yes"))
			}
			yes = true
		default:
			if strings.HasPrefix(arg, "-") || dir != "" {
				return fail(2, fmt.Errorf("無効な引数: %s", arg))
			}
			dir = arg
		}
	}
	if dir == "" || (cmd == "apply" && !yes) {
		return fail(2, fmt.Errorf("対象ディレクトリと、apply には --yes が必要です"))
	}
	info, e := os.Stat(dir)
	if e != nil {
		return fail(2, e)
	}
	if !info.IsDir() {
		return fail(2, fmt.Errorf("ディレクトリではありません: %s", dir))
	}
	var progress func(int, int, string)
	if f, ok := errout.(*os.File); ok {
		if isTerminal(f) {
			progress = func(done, total int, path string) { fmt.Fprintf(errout, "\r\033[2K[%d/%d] %s", done, total, path) }
		}
	}
	result, e := core.Scan(dir, progress)
	if progress != nil {
		fmt.Fprintln(errout)
	}
	if e != nil {
		if result != nil {
			table(out, result)
		}
		return fail(1, e)
	}
	if cmd == "apply" {
		changes, e := core.Plan(result)
		if e != nil {
			table(out, result)
			return fail(1, e)
		}
		if e = core.Commit(changes); e != nil {
			table(out, result)
			return fail(3, e)
		}
		for _, row := range result.Rows {
			if row.Target > 0 {
				row.Status = "置換済"
			}
		}
	}
	table(out, result)
	if cmd == "scan" {
		fmt.Fprintln(out, "\n候補一覧（パッケージ / 定数名 / 値 / 出現箇所数）")
		for _, c := range result.Candidates {
			fmt.Fprintf(out, "%s\t%s\t%q\t%d\n", c.Package, c.Name, c.Value, c.Count)
		}
		if len(result.Candidates) == 0 {
			fmt.Fprintln(out, "対象なし")
		}
	}
	return 0
}
func table(out io.Writer, result *core.Result) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ファイル\t検出\t除外\t対象\t状態")
	d, e, t := 0, 0, 0
	for _, r := range result.Rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n", r.Path, r.Detected, r.Excluded, r.Target, r.Status)
		d += r.Detected
		e += r.Excluded
		t += r.Target
	}
	fmt.Fprintf(w, "合計\t%d\t%d\t%d\t\n", d, e, t)
	_ = w.Flush()
}
