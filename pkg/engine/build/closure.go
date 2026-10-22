package build

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
)

// maxClosureInfo caps what one `nix path-info` may print (a closure of 10,000 paths is ~10 MB).
const maxClosureInfo = 256 << 20

// Closure implements engine.PlanCloser: outs are made valid (bob derivations of this run with
// RealiseNow, other derivations with one `nix build`) and their closure is read with one
// `nix path-info --recursive`.
func (s planStore) Closure(ctx context.Context, outs []engine.PlanOutput) (map[string][]string, error) {
	if len(outs) == 0 {
		return map[string][]string{}, nil
	}
	var paths []string
	byPath := map[string]engine.PlanOutput{}
	for _, o := range outs {
		paths = append(paths, o.Path)
		byPath[o.Path] = o
	}
	sort.Strings(paths)
	invalid, err := s.st.Invalid(ctx, paths)
	if err != nil {
		return nil, err
	}
	if len(invalid) > 0 && s.p != nil && s.p.dryRun {
		// --dry-run builds nothing (R-RS-005): the output generators plan their placeholder.
		return nil, fmt.Errorf("%w: a dry run builds nothing", engine.ErrClosureBuild)
	}
	bobDrvs := map[string]*drv.Derivation{}
	var nixTargets []string
	for _, p := range invalid {
		o := byPath[p]
		switch {
		case o.Drv == "":
			return nil, fmt.Errorf("%w: store path %s is not valid", engine.ErrClosureBuild, p)
		case s.p != nil && s.p.byPath[o.Drv] != nil:
			bobDrvs[o.Drv] = s.p.byPath[o.Drv]
		default:
			nixTargets = append(nixTargets, o.Drv+"^"+o.Name)
		}
	}
	if len(bobDrvs) > 0 {
		if _, err := s.RealiseNow(ctx, bobDrvs); err != nil {
			return nil, fmt.Errorf("%w: %v", engine.ErrClosureBuild, err)
		}
	}
	if len(nixTargets) > 0 {
		sort.Strings(nixTargets)
		args := []string{"build", "--no-link", "--keep-going"}
		if s.p != nil && s.p.maxJobs > 0 {
			args = append(args, "--max-jobs", strconv.Itoa(s.p.maxJobs))
		}
		if s.p != nil && s.p.offline {
			args = append(args, "--option", "substituters", "")
		}
		var log tailBuffer
		c := s.st.Command(ctx, "nix", append(args, nixTargets...)...)
		c.Stderr = &log
		if err := c.Run(); err != nil {
			return nil, fmt.Errorf("%w: building %v while planning failed (%v): %s", engine.ErrClosureBuild, nixTargets, err, log.tail())
		}
	}
	var out capBuffer
	out.max = maxClosureInfo
	var log tailBuffer
	c := s.st.Command(ctx, "nix", append([]string{"path-info", "--json", "--json-format", "1", "--recursive"}, paths...)...)
	c.Stdout, c.Stderr = &out, &log
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("reading the closure of %v: %v: %s", paths, err, log.tail())
	}
	if out.over {
		return nil, fmt.Errorf("reading the closure of %v: more than %d bytes of path information", paths, maxClosureInfo)
	}
	var info map[string]struct {
		References []string `json:"references"`
	}
	if err := json.Unmarshal(out.b, &info); err != nil {
		return nil, fmt.Errorf("reading the closure of %v: %w", paths, err)
	}
	res := make(map[string][]string, len(info))
	for p, i := range info {
		var refs []string
		for _, r := range i.References {
			if r != p {
				refs = append(refs, r)
			}
		}
		sort.Strings(refs)
		res[p] = refs
	}
	for _, p := range paths {
		if _, ok := res[p]; !ok {
			return nil, fmt.Errorf("reading the closure: nix path-info did not list %s", p)
		}
	}
	return res, nil
}

// capBuffer collects output up to max bytes and records an overflow.
type capBuffer struct {
	b    []byte
	max  int
	over bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if len(c.b)+len(p) > c.max {
		c.over = true
		return len(p), nil
	}
	c.b = append(c.b, p...)
	return len(p), nil
}
