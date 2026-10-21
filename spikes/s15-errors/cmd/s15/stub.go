package main

import (
	"flag"
	"fmt"
	"os"

	"bobspike/s15/translate"
)

func cmdReplay(args []string) (int, error) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	js := fs.Bool("json", false, "--json event stream")
	v := fs.Bool("v", false, "verbose (raw Nix messages)")
	color := fs.Bool("color", false, "TTY style (colors, glyphs)")
	_ = fs.Parse(args)
	f, stream, err := translate.LoadFixture(fs.Arg(0))
	if err != nil {
		return 0, err
	}
	r, evs := translate.Replay(f, stream)
	if *js {
		for i := range evs {
			if evs[i].Error != nil && !*v {
				c := *evs[i].Error
				c.Raw = nil
				evs[i].Error = &c
			}
		}
		return r.ExitCode, translate.JSONLines(os.Stdout, evs)
	}
	fmt.Print(translate.HumanString(r, translate.Style{Color: *color, Glyphs: *color, Verbose: *v}))
	return r.ExitCode, nil
}
