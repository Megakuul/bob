package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"bobspike/s15/nixlog"
	"bobspike/s15/translate"
)

type typeRow struct {
	Key    string         `json:"key"`    // action/type
	Name   string         `json:"name"`   // activity or result name
	Levels map[int]int    `json:"levels"` // level -> count (start/msg)
	Count  map[string]int `json:"count"`  // nix version -> count
	Faults []string       `json:"faults"` // fixtures where it occurs
	Fields string         `json:"fields"` // field types of the first sample
	Sample string         `json:"sample"` // first raw line (ANSI stripped, truncated)
}

var hashRe = regexp.MustCompile(`[0-9a-z]{32}`)

// cmdTypes catalogues every action/activity/result type and every level-0/1 message shape.
func cmdTypes(args []string) error {
	root := "fixtures"
	if len(args) > 0 {
		root = args[0]
	}
	rows := map[string]*typeRow{}
	msgs := map[string]*typeRow{}
	dirs, _ := filepath.Glob(filepath.Join(root, "*", "*"))
	for _, d := range dirs {
		f, stream, err := translate.LoadFixture(d)
		if err != nil || stream == nil {
			continue
		}
		fault := filepath.Base(filepath.Dir(d))
		lines := strings.Split(string(stream), "\n")
		for _, l := range lines {
			if l == "" {
				continue
			}
			ev := nixlog.ParseLine(l)
			var key, name string
			switch {
			case ev.Plain:
				key, name = "plain", "non-JSON stderr line"
			case ev.Action == "start":
				key, name = fmt.Sprintf("start/%d", ev.Type), ev.Activity().String()
			case ev.Action == "result":
				key, name = fmt.Sprintf("result/%d", ev.Type), ev.Result().String()
			case ev.Action == "stop":
				key, name = "stop", "stop"
			case ev.Action == "msg":
				key, name = fmt.Sprintf("msg/level%d", ev.Level), "msg"
				if ev.File != nil || ev.RawMsg != "" {
					key += "/error-info"
				}
			default:
				key, name = "action/"+string(ev.Action), "UNKNOWN"
			}
			add(rows, key, name, ev, f.Nix, fault, l)
			if ev.Action == "msg" && ev.Level <= 1 {
				t := ev.RawMsg
				if t == "" {
					t = ev.Msg
				}
				t = translate.StripANSI(t)
				shape := hashRe.ReplaceAllString(firstWords(t, 7), "H")
				add(msgs, fmt.Sprintf("L%d %s", ev.Level, shape), "", ev, f.Nix, fault, l)
			}
		}
	}
	out := map[string]any{"types": sorted(rows), "messages": sorted(msgs)}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "catalogue.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	for _, r := range sorted(rows) {
		fmt.Printf("| `%s` | %s | %v | %v | %d faults | `%s` |\n", r.Key, r.Name, r.Count, levels(r.Levels), len(r.Faults), r.Fields)
	}
	fmt.Println()
	for _, r := range sorted(msgs) {
		fmt.Printf("| %s | %v | %s |\n", r.Key, r.Count, strings.Join(r.Faults, ", "))
	}
	return nil
}

func levels(m map[int]int) string {
	var ks []string
	for k := range m {
		ks = append(ks, fmt.Sprint(k))
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func firstWords(s string, n int) string {
	s, _, _ = strings.Cut(s, "\n")
	w := strings.Fields(s)
	if len(w) > n {
		w = w[:n]
	}
	return strings.Join(w, " ")
}

func add(m map[string]*typeRow, key, name string, ev nixlog.Event, nix, fault, raw string) {
	r := m[key]
	if r == nil {
		raw = translate.StripANSI(raw)
		if len(raw) > 300 {
			raw = raw[:300] + "…"
		}
		var ft []string
		for _, f := range ev.Fields {
			switch {
			case len(f) > 0 && f[0] == '"':
				ft = append(ft, "str")
			default:
				ft = append(ft, "int")
			}
		}
		r = &typeRow{Key: key, Name: name, Levels: map[int]int{}, Count: map[string]int{}, Fields: "[" + strings.Join(ft, ",") + "]", Sample: raw}
		m[key] = r
	}
	r.Count[nix]++
	if ev.Action == "start" || ev.Action == "msg" {
		r.Levels[int(ev.Level)]++
	}
	if !contains(r.Faults, fault) {
		r.Faults = append(r.Faults, fault)
	}
}

func sorted(m map[string]*typeRow) []*typeRow {
	var out []*typeRow
	for _, k := range sortedKeys(m) {
		sort.Strings(m[k].Faults)
		out = append(out, m[k])
	}
	return out
}
