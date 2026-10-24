// s08: spike CLI. `s08 oci spec.json` builds an OCI layout with a multi-platform index;
// `s08 appimage spec.json` builds an AppImage.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"bobspike/s08/appimage"
	"bobspike/s08/oci"
)

type ociSpec struct {
	Out    string      `json:"out"`
	Ref    string      `json:"ref"`
	Images []oci.Image `json:"images"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "s08:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: s08 oci|appimage spec.json")
	}
	b, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	ctx := context.Background()
	t0 := time.Now()
	switch os.Args[1] {
	case "oci":
		var s ociSpec
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		l, err := oci.OpenLayout(s.Out)
		if err != nil {
			return err
		}
		var ds []oci.Descriptor
		for _, img := range s.Images {
			t := time.Now()
			d, st, err := l.Build(ctx, img)
			if err != nil {
				return fmt.Errorf("%s: %w", img.Arch, err)
			}
			fmt.Printf("image %s %s layers=%d reused=%d bytes=%d time=%.2fs\n", img.Arch, d.Digest, st.Layers, st.Reused, st.LayerBytes, time.Since(t).Seconds())
			ds = append(ds, d)
		}
		d, err := l.Index(s.Ref, ds...)
		if err != nil {
			return err
		}
		fmt.Printf("index %s total=%.2fs\n", d.Digest, time.Since(t0).Seconds())
	case "appimage":
		var s appimage.Bundle
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		st, err := appimage.Build(ctx, s)
		if err != nil {
			return err
		}
		fmt.Printf("appimage %s sha256=%s size=%d closure=%d paths=%d time=%.2fs\n", s.Out, st.SHA256, st.Size, st.ClosureBytes, st.Paths, time.Since(t0).Seconds())
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
	return nil
}
