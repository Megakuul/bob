package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// Determinism (R-FW-008): bob check --determinism evaluates twice, each run in its own sandbox,
// and compares the IR bytes.

// CheckDeterminism evaluates r twice and returns BOB-E-1018 (as ir.Errors) when the two IRs
// differ. The second evaluation reuses the evaluator binary: the difference can only come from
// running it.
func CheckDeterminism(ctx context.Context, r Request) error {
	a, err := Evaluate(ctx, r)
	if err != nil {
		return err
	}
	b, err := Evaluate(ctx, r)
	if err != nil {
		return err
	}
	return Compare(a.IR, b.IR)
}

// Compare returns nil for identical IR documents, and otherwise BOB-E-1018 naming the first
// node (by id) that differs and the JSON path of the first differing field.
func Compare(a, b []byte) error {
	if bytes.Equal(a, b) {
		return nil
	}
	var da, db map[string]any
	if json.Unmarshal(a, &da) != nil || json.Unmarshal(b, &db) != nil {
		return ir.Errors{ir.Nondeterministic("the build graph", "$")}
	}
	na, nb := nodesByID(da), nodesByID(db)
	ids := make([]string, 0, len(na)+len(nb))
	for id := range na {
		ids = append(ids, id)
	}
	for id := range nb {
		if _, ok := na[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		x, y := na[id], nb[id]
		if x.index < 0 || y.index < 0 {
			label := x.label
			if label == "" {
				label = y.label
			}
			return ir.Errors{ir.Nondeterministic(label, "nodes")}
		}
		if p := firstDiff(x.node, y.node, fmt.Sprintf("nodes[%d]", x.index)); p != "" {
			return ir.Errors{ir.Nondeterministic(x.label, p)}
		}
	}
	delete(da, "nodes")
	delete(db, "nodes")
	p := firstDiff(da, db, "")
	if p == "" {
		p = "$"
	}
	return ir.Errors{ir.Nondeterministic("the build graph", p)}
}

type irNode struct {
	index int
	label string
	node  any
}

func nodesByID(doc map[string]any) map[string]irNode {
	out := map[string]irNode{}
	list, _ := doc["nodes"].([]any)
	for i, n := range list {
		m, _ := n.(map[string]any)
		id, _ := m["id"].(string)
		label, _ := m["label"].(string)
		if label == "" {
			label = id
		}
		out[id] = irNode{index: i, label: label, node: n}
	}
	return out
}

// firstDiff returns the JSON path (relative to at) of the first difference between a and b, ""
// if they are equal.
func firstDiff(a, b any, at string) string {
	join := func(k string) string {
		if at == "" {
			return k
		}
		return at + "." + k
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return orRoot(at)
		}
		keys := make([]string, 0, len(x)+len(y))
		for k := range x {
			keys = append(keys, k)
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p := firstDiff(x[k], y[k], join(k)); p != "" {
				return p
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok {
			return orRoot(at)
		}
		for i := 0; i < len(x) && i < len(y); i++ {
			if p := firstDiff(x[i], y[i], at+"["+strconv.Itoa(i)+"]"); p != "" {
				return p
			}
		}
		if len(x) != len(y) {
			return orRoot(at)
		}
		return ""
	default:
		xb, _ := json.Marshal(a)
		yb, _ := json.Marshal(b)
		if !bytes.Equal(xb, yb) {
			return orRoot(at)
		}
		return ""
	}
}

func orRoot(at string) string {
	if at == "" {
		return "$"
	}
	return at
}
