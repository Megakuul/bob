//go:build bob

package bob_test

import "github.com/megakuul/bob/pkg/bob"

func ExampleRun() {
	_ = bob.Define(func(p *bob.P) *bob.Step {
		return bob.Run(p, "version-file", bob.RunOpts{
			Cmd:     []string{"write-version", "version.txt"},
			Outputs: []string{"version.txt"},
		})
	})
}

func ExampleFetch() {
	_ = bob.Define(func(p *bob.P) bob.Tree {
		return bob.Fetch(p, bob.FetchOpts{URL: "https://example.org/fonts-1.2.tar.gz", Archive: true, Strip: 1})
	})
}

func ExampleNewShell() {
	_ = bob.Define(func(p *bob.P) *bob.Shell {
		return bob.NewShell(p, bob.DevShell{Env: map[string]string{"APP_ENV": "dev"}})
	})
}
