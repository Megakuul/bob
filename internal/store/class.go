package store

import (
	"crypto/sha256"
	"slices"
	"strings"
)

// Class is a scheduling class: the platform a job needs plus the capabilities and labels the
// executing worker must have (R-FLEET-004). The class id is the queue key `qs/<classID>/…`.
type Class struct {
	Platform string   // "x86_64-linux" | "aarch64-linux"
	Caps     []string // capabilities a worker must provide
	Labels   []string // labels a worker must carry
}

// ClassID is `<platform>.<first 10 base32 chars of SHA-256(canonical caps ‖ labels)>`, for
// example "aarch64-linux.k3j9d0a1b2". The id does not depend on the order of Caps or Labels.
//
// Canonical form: caps sorted and deduplicated, one per line, then a 0 byte, then the labels
// the same way, so that a cap can never be read as a label.
func ClassID(c Class) (string, error) {
	if _, err := Segment(c.Platform); err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(canonical(c.Caps)))
	h.Write([]byte{0})
	h.Write([]byte(canonical(c.Labels)))
	sum := strings.ToLower(b32.EncodeToString(h.Sum(nil)))
	return Segment(c.Platform + "." + sum[:10])
}

func canonical(xs []string) string {
	s := slices.Clone(xs)
	slices.Sort(s)
	s = slices.Compact(s)
	var b strings.Builder
	for _, x := range s {
		b.WriteString(x)
		b.WriteByte('\n')
	}
	return b.String()
}

// WorkerProfile is what a worker offers.
type WorkerProfile struct {
	Platform string
	Caps     []string
	Labels   []string
}

// Satisfies reports whether a worker with profile p may run jobs of class c: same platform,
// every capability and every label of the class present.
func Satisfies(p WorkerProfile, c Class) (bool, error) {
	if p.Platform != c.Platform {
		return false, nil
	}
	for _, x := range c.Caps {
		if !slices.Contains(p.Caps, x) {
			return false, nil
		}
	}
	for _, x := range c.Labels {
		if !slices.Contains(p.Labels, x) {
			return false, nil
		}
	}
	return true, nil
}
