package main

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"bobspike/s11/internal/reg"
)

// runBench times the worker-side and orchestrator-side crypto per suite.
func runBench() int {
	pt := make([]byte, 600) // token envelope with STS credentials is ~500-700 bytes
	for _, s := range []reg.Suite{reg.SuiteX25519, reg.SuiteMLKEM768X25519} {
		const n = 2000
		var k *reg.WorkerKeys
		t := time.Now()
		for range n {
			k, _ = reg.NewWorkerKeys(s)
		}
		kg := time.Since(t) / n
		var ct []byte
		t = time.Now()
		for range n {
			ct, _ = reg.Seal(s, k.KEMPub, k.ID(), 0, 1, pt)
		}
		se := time.Since(t) / n
		t = time.Now()
		for range n {
			k.Open(k.ID(), 0, 1, ct)
		}
		op := time.Since(t) / n
		msg := make([]byte, 300)
		sig := ed25519.Sign(k.Sig, msg)
		t = time.Now()
		for range n {
			ed25519.Verify(k.SigPub(), msg, sig)
		}
		ve := time.Since(t) / n
		fmt.Printf("| 0x%04x | %d | %d | %s | %s | %s | %s |\n", uint16(s), len(k.KEMPub), len(ct), kg, se, op, ve)
	}
	return 0
}
