package logstore

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// idAt builds a build id whose time is t (random part fixed by tail).
func idAt(t time.Time, tail string) string {
	ms := uint64(t.UnixMilli())
	var b [10]byte
	for i := 9; i >= 0; i-- {
		b[i] = crockford[ms&31]
		ms >>= 5
	}
	r := []byte((tail + "0000000000000000")[:16])
	for i, c := range r {
		r[i] = crockford[c%32]
	}
	return string(b[:]) + string(r)
}

func TestPartitions(t *testing.T) {
	at := time.Date(2026, 10, 22, 13, 4, 5, 0, time.UTC)
	id := idAt(at, "abc")
	got, err := BuildTime(id)
	if err != nil || !got.Equal(at) {
		t.Fatalf("BuildTime(%s) = %v, %v; want %v", id, got, err, at)
	}
	p, err := LogPartition(id, 4)
	if err != nil || !strings.HasPrefix(p, "log-20261022-") || len(p) != len("log-20261022-0") {
		t.Fatalf("LogPartition = %q, %v", p, err)
	}
	if p2, _ := LogPartition(id, 4); p2 != p {
		t.Fatalf("LogPartition not stable: %s %s", p, p2)
	}
	if r, _ := RunPartition(id); r != "run-202610" {
		t.Fatalf("RunPartition = %q", r)
	}
	if j := JobPartition(at); j != "job-202610" {
		t.Fatalf("JobPartition = %q", j)
	}
	for _, bad := range []string{"", "short", strings.Repeat("u", 26), strings.Repeat("Z", 26), "../../../../../../../../../", strings.Repeat("z", 26)} {
		if _, err := LogPartition(bad, 4); err == nil {
			t.Errorf("LogPartition(%q) accepted", bad)
		}
	}
	for name, want := range map[string]string{
		"log-20261022-3": "2026-10-23", "run-202612": "2027-01-01", "job-202601": "2026-02-01",
	} {
		end, ok := PartitionEnd(name)
		if !ok || end.Format("2006-01-02") != want {
			t.Errorf("PartitionEnd(%s) = %v %v, want %s", name, end, ok, want)
		}
	}
	for _, bad := range []string{"log-2026102-1", "log-20261022", "log-20261022-x", "x-202601", "run-2026"} {
		if _, ok := PartitionEnd(bad); ok {
			t.Errorf("PartitionEnd(%s) accepted", bad)
		}
	}
}

func TestSplitText(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes, 2-byte runes
	for _, c := range []struct {
		in   string
		want int
	}{{"", 1}, {"abc", 1}, {strings.Repeat("A", 4096), 1}, {strings.Repeat("A", 4097), 2}, {strings.Repeat("A", 100000), 25}, {long, 2}, {"a\xffb", 1}} {
		parts := SplitText(c.in)
		if len(parts) != c.want {
			t.Errorf("SplitText(%d bytes) = %d parts, want %d", len(c.in), len(parts), c.want)
		}
		var joined strings.Builder
		for _, p := range parts {
			if len(p) > MaxLineBytes || !utf8.ValidString(p) {
				t.Errorf("part of %d bytes, valid %v", len(p), utf8.ValidString(p))
			}
			joined.WriteString(p)
		}
		if utf8.ValidString(c.in) && joined.String() != c.in {
			t.Errorf("SplitText lost bytes")
		}
	}
}

func ln(pkg string, att int32, seq int64) Line {
	return Line{Package: pkg, Attempt: att, Seq: seq, Text: pkg + "/" + string(rune('0'+att)) + "/" + itoa(seq)}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestSettle: dedupe by (package, attempt, seq), the restart rule, (package, seq) order.
func TestSettle(t *testing.T) {
	var in []Line
	for s := int64(1); s <= 10; s++ { // attempt 1 lines 1..10, committed twice (ambiguous commit)
		in = append(in, ln("//b:x", 1, s), ln("//b:x", 1, s))
	}
	for s := int64(8); s <= 12; s++ { // attempt 2 restarted at 8
		in = append(in, ln("//b:x", 2, s))
	}
	for s := int64(3); s >= 1; s-- {
		in = append(in, ln("//a:x", 1, s))
	}
	out, rs := settle(in)
	var got []string
	for _, l := range out {
		got = append(got, l.Text)
	}
	want := "//a:x/1/1 //a:x/1/2 //a:x/1/3 //b:x/1/1 //b:x/1/2 //b:x/1/3 //b:x/1/4 //b:x/1/5 //b:x/1/6 //b:x/1/7 //b:x/2/8 //b:x/2/9 //b:x/2/10 //b:x/2/11 //b:x/2/12"
	if strings.Join(got, " ") != want {
		t.Fatalf("settle:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if len(rs) != 1 || rs[0] != (Restart{Package: "//b:x", Attempt: 2, FromSeq: 8}) {
		t.Fatalf("restarts = %+v", rs)
	}
}

// TestMerger feeds the S17 cases: out-of-order and duplicate lines from two sources, a
// restart after delivered lines (the follower must get a Restart and the new attempt), and a
// follower that resumes from a cursor.
func TestMerger(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	m := newMerger(nil, clock)
	for _, s := range []int64{3, 1, 2, 2, 5} {
		m.offer(ln("p", 1, s))
	}
	m.drain()
	ls, _ := m.take()
	if len(ls) != 3 || ls[2].Seq != 3 {
		t.Fatalf("first drain = %+v", ls)
	}
	now = now.Add(2 * time.Second)
	if !m.gapOlderThan(time.Second) {
		t.Fatalf("seq 4 missing for 2 s: no gap reported")
	}
	// attempt 2 restarts at 3: the delivered seq 3 of attempt 1 is void
	m.restart(Restart{Package: "p", Attempt: 2, FromSeq: 3})
	for _, s := range []int64{3, 4} {
		m.offer(ln("p", 2, s))
	}
	m.offer(ln("p", 1, 4)) // late line of the old attempt: void
	m.drain()
	ls, rs := m.take()
	if len(rs) != 1 || rs[0].FromSeq != 3 {
		t.Fatalf("restart not sent: %+v", rs)
	}
	if len(ls) != 2 || ls[0].Attempt != 2 || ls[0].Seq != 3 || ls[1].Seq != 4 {
		t.Fatalf("after restart = %+v", ls)
	}
	if m.gapOlderThan(time.Second) {
		t.Fatalf("gap still reported after the restart filled it")
	}
	if f := m.finals(); len(f) != 1 || f[0] != (Final{Package: "p", LastSeq: 4, Attempt: 2}) {
		t.Fatalf("finals = %+v", f)
	}

	// resume from a cursor: nothing at or below it is delivered again
	r := newMerger(map[string]Cursor{"p": {Seq: 2, Attempt: 1}}, clock)
	for _, s := range []int64{1, 2, 3} {
		r.offer(ln("p", 1, s))
	}
	r.drain()
	if ls, _ := r.take(); len(ls) != 1 || ls[0].Seq != 3 {
		t.Fatalf("resume = %+v", ls)
	}
}
