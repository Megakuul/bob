package buildctl

import (
	"regexp"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/logstore"
)

func TestBuildIDs(t *testing.T) {
	at := time.Date(2026, 10, 22, 13, 4, 5, 6e6, time.UTC)
	id, err := NewBuildID(at)
	if err != nil {
		t.Fatal(err)
	}
	// SPEC §02.6 (`b-01JB7Q8V3K`) and the R-DEV-005 wait pattern
	if !regexp.MustCompile(`^b-[0-9A-HJKMNP-TV-Z]{26}$`).MatchString(id) || !regexp.MustCompile(`b-[0-9A-Z]{10,}`).MatchString(id) {
		t.Fatalf("NewBuildID = %q", id)
	}
	for name, parse := range map[string]func(string) (time.Time, error){"buildTime": buildTime, "logstore.BuildTime": logstore.BuildTime} {
		if got, err := parse(id); err != nil || !got.Equal(at) {
			t.Errorf("%s(%s) = %v, %v; want %v", name, id, got, err, at)
		}
		// ids of earlier versions (26 lower-case characters) stay readable
		if got, err := parse("01jbq7k2p4m8x0c3v5n7r9t1y6"); err != nil || got.Year() != 2024 {
			t.Errorf("%s(legacy) = %v, %v", name, got, err)
		}
		for _, bad := range []string{"", "b-", "b-01jbq7k2p4m8x0c3v5n7r9t1y6", "01JBQ7K2P4M8X0C3V5N7R9T1Y6", "b-01JBQ7K2P4M8X0C3V5N7R9T1Y", "b-../../../../../../../../../..", "b-ZZZZZZZZZZZZZZZZZZZZZZZZZZ", "b-01JBQ7K2P4M8X0C3V5N7R9T1YU"} {
			if _, err := parse(bad); err == nil {
				t.Errorf("%s(%q) accepted", name, bad)
			}
		}
	}
	later, _ := NewBuildID(at.Add(time.Millisecond))
	if later <= id {
		t.Errorf("ids do not sort by time: %s then %s", id, later)
	}
	j := JobID(id, 7)
	if j != id+".j0007" {
		t.Errorf("JobID = %s", j)
	}
	if b, ok := BuildOfJob(j); !ok || b != id {
		t.Errorf("BuildOfJob(%s) = %s, %v", j, b, ok)
	}
	for _, bad := range []string{id, id + ".j7", id + ".jabcd", "b-x.j0001", "01jbq7k2p4m8x0c3v5n7r9t1y6.j0001x"} {
		if _, ok := BuildOfJob(bad); ok {
			t.Errorf("BuildOfJob(%q) accepted", bad)
		}
	}
}
