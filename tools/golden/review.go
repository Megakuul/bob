package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// op is one line of a diff: ' ' kept, '-' only in old, '+' only in new.
type op struct {
	Kind byte
	Text string
}

// maxDiffCells bounds the LCS table; beyond it the diff degrades to delete-all/insert-all.
const maxDiffCells = 16 << 20

// diffLines is a plain LCS line diff. Goldens are small; clarity beats speed here.
func diffLines(a, b []string) []op {
	n, m := len(a), len(b)
	if (n+1)*(m+1) > maxDiffCells {
		ops := make([]op, 0, n+m)
		for _, l := range a {
			ops = append(ops, op{'-', l})
		}
		for _, l := range b {
			ops = append(ops, op{'+', l})
		}
		return ops
	}
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}

// canonical pretty-prints and key-sorts JSON (and each JSONL line) so diffs show content
// changes, not formatting. Anything that does not parse is shown as is.
func canonical(id string, b []byte) []byte {
	pretty := func(in []byte) ([]byte, bool) {
		d := json.NewDecoder(bytes.NewReader(in))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil || d.More() {
			return nil, false
		}
		out, err := json.MarshalIndent(v, "", "  ") // map keys are sorted by encoding/json
		return out, err == nil
	}
	switch {
	case strings.HasSuffix(id, ".json"):
		if out, ok := pretty(b); ok {
			return append(out, '\n')
		}
	case strings.HasSuffix(id, ".jsonl"):
		var buf bytes.Buffer
		for _, l := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
			out, ok := pretty(l)
			if !ok {
				return b
			}
			buf.Write(out)
			buf.WriteByte('\n')
		}
		return buf.Bytes()
	}
	return b
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// row is one side-by-side row.
type row struct {
	OldNo, NewNo int
	Old, New     string
	Kind         string // same, del, add, chg
}

func sideBySide(ops []op) []row {
	var rows []row
	on, nn := 0, 0
	for k := 0; k < len(ops); {
		if ops[k].Kind == ' ' {
			on++
			nn++
			rows = append(rows, row{on, nn, ops[k].Text, ops[k].Text, "same"})
			k++
			continue
		}
		// Pair a run of deletions with the following run of insertions.
		var dels, adds []string
		for k < len(ops) && ops[k].Kind == '-' {
			dels = append(dels, ops[k].Text)
			k++
		}
		for k < len(ops) && ops[k].Kind == '+' {
			adds = append(adds, ops[k].Text)
			k++
		}
		for x := 0; x < len(dels) || x < len(adds); x++ {
			r := row{Kind: "chg"}
			if x < len(dels) {
				on++
				r.OldNo, r.Old = on, dels[x]
			} else {
				r.Kind = "add"
			}
			if x < len(adds) {
				nn++
				r.NewNo, r.New = nn, adds[x]
			} else {
				r.Kind = "del"
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// view is what the template renders for one entry.
type view struct {
	ID, Anchor, Test, Status string
	Reqs                     []string
	Binary                   bool
	OldImg, NewImg, DiffImg  string
	Note                     string
	Unified                  []op
	Rows                     []row
}

func writeReview(out string, es []entry) (string, error) {
	if err := os.RemoveAll(out); err != nil {
		return "", fmt.Errorf("clean review dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(out, "img"), 0o755); err != nil {
		return "", fmt.Errorf("review dir: %w", err)
	}
	views := make([]view, 0, len(es))
	for i, e := range es {
		v := view{ID: e.ID, Anchor: fmt.Sprintf("g%d", i), Test: e.Test, Reqs: e.Reqs, Status: "changed"}
		if !e.exists {
			v.Status = "new"
		}
		switch {
		case strings.HasSuffix(e.ID, ".png"):
			v.Binary = true
			if err := pngViews(out, &v, e); err != nil {
				v.Note = err.Error()
			}
		case !isText(e.Old) || !isText(e.New):
			v.Binary = true
			v.Note = fmt.Sprintf("binary content: %d bytes golden, %d bytes candidate", len(e.Old), len(e.New))
		default:
			ops := diffLines(splitLines(canonical(e.ID, e.Old)), splitLines(canonical(e.ID, e.New)))
			v.Unified, v.Rows = ops, sideBySide(ops)
		}
		views = append(views, v)
	}
	var buf bytes.Buffer
	if err := page.Execute(&buf, views); err != nil {
		return "", fmt.Errorf("render review: %w", err)
	}
	idx := filepath.Join(out, "index.html")
	if err := os.WriteFile(idx, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("write review: %w", err)
	}
	return idx, nil
}

func isText(b []byte) bool { return !bytes.ContainsRune(b, 0) }

// pngViews writes old, new and a red-highlight difference image next to the page.
func pngViews(out string, v *view, e entry) error {
	write := func(name string, b []byte) (string, error) {
		rel := "img/" + v.Anchor + "-" + name + ".png"
		return rel, os.WriteFile(filepath.Join(out, filepath.FromSlash(rel)), b, 0o644)
	}
	var err error
	if v.NewImg, err = write("new", e.New); err != nil {
		return fmt.Errorf("write image: %w", err)
	}
	if !e.exists {
		return nil
	}
	if v.OldImg, err = write("old", e.Old); err != nil {
		return fmt.Errorf("write image: %w", err)
	}
	oi, err := png.Decode(bytes.NewReader(e.Old))
	if err != nil {
		return fmt.Errorf("decode golden png: %w", err)
	}
	ni, err := png.Decode(bytes.NewReader(e.New))
	if err != nil {
		return fmt.Errorf("decode candidate png: %w", err)
	}
	b := oi.Bounds().Union(ni.Bounds())
	d := image.NewRGBA(b)
	diff := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			p := image.Pt(x, y)
			in := p.In(oi.Bounds()) && p.In(ni.Bounds())
			if in && color.RGBAModel.Convert(oi.At(x, y)) == color.RGBAModel.Convert(ni.At(x, y)) {
				g := color.GrayModel.Convert(ni.At(x, y)).(color.Gray)
				l := 128 + g.Y/2 // faded context
				d.Set(x, y, color.RGBA{l, l, l, 255})
				continue
			}
			diff++
			d.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, d); err != nil {
		return fmt.Errorf("encode diff png: %w", err)
	}
	if v.DiffImg, err = write("diff", buf.Bytes()); err != nil {
		return fmt.Errorf("write image: %w", err)
	}
	v.Note = fmt.Sprintf("%d differing pixels", diff)
	return nil
}

var page = template.Must(template.New("review").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Golden review</title>
<style>
:root{--bg:#fff;--fg:#1a1a1a;--mute:#666;--line:#ddd;--del:#fde8e8;--add:#e6f6e6;--delw:#b42318;--addw:#067647}
@media (prefers-color-scheme: dark){:root{--bg:#141414;--fg:#e8e8e8;--mute:#999;--line:#333;--del:#3a1d1d;--add:#16301b;--delw:#f97066;--addw:#47cd89}}
body{background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,sans-serif;margin:0;padding:16px}
h1{font-size:20px;margin:0 0 8px} h2{font-size:15px;margin:24px 0 4px;font-family:ui-monospace,monospace;word-break:break-all}
.meta{color:var(--mute);margin-bottom:8px} ul{padding-left:20px}
table{border-collapse:collapse;width:100%;font:12px/1.4 ui-monospace,monospace;table-layout:fixed}
td{border-top:1px solid var(--line);padding:0 6px;white-space:pre-wrap;word-break:break-all;vertical-align:top}
td.n{width:3.5em;color:var(--mute);text-align:right}
.del{background:var(--del)} .add{background:var(--add)}
pre{font:12px/1.4 ui-monospace,monospace;border:1px solid var(--line);padding:8px;overflow-x:auto}
pre .d{color:var(--delw)} pre .a{color:var(--addw)}
details{margin:6px 0} .imgs{display:flex;gap:12px;flex-wrap:wrap} .imgs figure{margin:0;max-width:100%} .imgs img{max-width:100%;border:1px solid var(--line)}
.wrap{overflow-x:auto}
</style></head><body>
<h1>Golden review</h1>
<p class="meta">{{len .}} changed golden(s). Approve with <code>go run ./tools/golden approve &lt;id|all&gt;</code>.</p>
<ul>{{range .}}<li><a href="#{{.Anchor}}">{{.ID}}</a> ({{.Status}})</li>{{end}}</ul>
{{range .}}<section id="{{.Anchor}}">
<h2>{{.ID}}</h2>
<div class="meta">{{.Status}}{{if .Test}} · test {{.Test}}{{end}}{{if .Reqs}} · {{range $i, $r := .Reqs}}{{if $i}}, {{end}}{{$r}}{{end}}{{end}}{{if .Note}} · {{.Note}}{{end}}</div>
{{if .Binary}}<div class="imgs">{{if .OldImg}}<figure><img src="{{.OldImg}}" alt="golden"><figcaption>golden</figcaption></figure>{{end}}{{if .NewImg}}<figure><img src="{{.NewImg}}" alt="candidate"><figcaption>candidate</figcaption></figure>{{end}}{{if .DiffImg}}<figure><img src="{{.DiffImg}}" alt="difference"><figcaption>difference</figcaption></figure>{{end}}</div>
{{else}}<div class="wrap"><table>{{range .Rows}}<tr><td class="n">{{if .OldNo}}{{.OldNo}}{{end}}</td><td class="{{if eq .Kind "del" "chg"}}del{{end}}">{{.Old}}</td><td class="n">{{if .NewNo}}{{.NewNo}}{{end}}</td><td class="{{if eq .Kind "add" "chg"}}add{{end}}">{{.New}}</td></tr>{{end}}</table></div>
<details><summary>unified diff</summary><pre>{{range .Unified}}{{if eq .Kind 45}}<span class="d">-{{.Text}}</span>{{else if eq .Kind 43}}<span class="a">+{{.Text}}</span>{{else}} {{.Text}}{{end}}
{{end}}</pre></details>{{end}}
</section>{{end}}
</body></html>
`))
