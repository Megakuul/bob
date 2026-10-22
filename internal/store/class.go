package store

import "github.com/megakuul/bob/pkg/bob/diag"

// Class is a scheduling class: the platform a job needs plus the capabilities and labels the
// executing worker must have (R-FLEET-004). The class id is the queue key `qs/<classID>/…`.
type Class struct {
	Platform string   // "x86_64-linux" | "aarch64-linux"
	Caps     []string // capabilities a worker must provide
	Labels   []string // labels a worker must carry
}

// ClassID is `<platform>.<first 10 base32 chars of SHA-256(canonical caps ‖ labels)>`, for
// example "aarch64-linux.k3j9d0a1b2". The id does not depend on the order of Caps or Labels.
func ClassID(c Class) (string, error) { return "", diag.NotImplemented("store.ClassID") }

// WorkerProfile is what a worker offers.
type WorkerProfile struct {
	Platform string
	Caps     []string
	Labels   []string
}

// Satisfies reports whether a worker with profile p may run jobs of class c: same platform,
// every capability and every label of the class present.
func Satisfies(p WorkerProfile, c Class) (bool, error) {
	return false, diag.NotImplemented("store.Satisfies")
}
