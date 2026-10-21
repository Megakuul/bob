package translate

import (
	"bytes"
	"testing"
)

// BenchmarkReplayCold replays the largest capture (cold store: every toolchain substituted
// from cache.nixos.org and the whole fixture built) through parser + translator.
func BenchmarkReplayCold(b *testing.B) {
	f, stream, err := LoadFixture("../fixtures/ok-cold/2.34.9")
	if err != nil {
		b.Skip(err)
	}
	lines := bytes.Count(stream, []byte("\n"))
	b.SetBytes(int64(len(stream)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Replay(f, stream)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(lines), "ns/line")
}
