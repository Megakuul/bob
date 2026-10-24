package promote

import (
	"context"
	"errors"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
)

// bound ties every staged path to the job (I-R2 F1, decision A391): a path must be an output
// of a package in the job's evaluation report (<staging>eval.json, bound to the owner's
// signed result by JobResult.EvalSHA) or lie in the reference closure of those outputs among
// the staged paths. Anything else is not this job's to publish.
//
// The report is written by the worker that built the job (evalbuild self-attestation), so
// this narrows what a worker can stage to what its own evaluation of the job names; it does
// not prove that evaluation. Cross-project safety does not depend on it: a published path
// never gains another project's visibility with other bytes (alreadyServed), and a release
// only publishes paths of its own project (internal/release).
func (p *Promoter) bound(ctx context.Context, j *Job, paths []*stagedPath) error {
	if len(paths) == 0 {
		return nil
	}
	if j.evalSHA == "" {
		return verifyFailed("the job result names no evaluation report, so no staged path is bound to the job")
	}
	key := jobspec.ReportKey(j.Staging)
	body, _, err := p.c.Objects.Read(ctx, key, jobspec.MaxReport)
	switch {
	case errors.Is(err, cache.ErrNotFound):
		return verifyFailed("the evaluation report %s is missing", key)
	case errors.Is(err, cache.ErrTooLarge):
		return verifyFailed("the evaluation report %s is too large", key)
	case err != nil:
		return err
	}
	rep, err := jobspec.ParseReport(body, j.evalSHA)
	if err != nil {
		return verifyFailed("%v", err)
	}
	byHash := map[string]*stagedPath{}
	for _, sp := range paths {
		byHash[sp.hash] = sp
	}
	reached := map[string]bool{}
	var queue []string
	for _, pkg := range rep.Packages {
		for _, out := range pkg.Outputs {
			h := cache.HashOf(out)
			if sp := byHash[h]; sp != nil && sp.path == out && !reached[h] {
				reached[h] = true
				queue = append(queue, h)
			}
		}
	}
	for len(queue) > 0 {
		sp := byHash[queue[0]]
		queue = queue[1:]
		for _, ref := range sp.claim.References {
			h := cache.HashOf(ref)
			if byHash[h] != nil && !reached[h] {
				reached[h] = true
				queue = append(queue, h)
			}
		}
	}
	for _, sp := range paths {
		if !reached[sp.hash] {
			return verifyFailed("staged path %s is neither an output of this job's packages nor in their closure", sp.path)
		}
	}
	return nil
}

// upstreamOf verifies that the skipped path with store hash h is an upstream path: its
// narinfo, staged by the worker from the upstream cache at <staging>upstream/<h>.narinfo, names
// that path and carries a signature by one of UpstreamKeys (I-R2 F3, decision A393). The
// worker's own list is never taken on trust: unverified, a skipped path is just a path of no
// one's (it neither excuses a reference nor a store hash inside a kinds output).
func (p *Promoter) upstreamOf(ctx context.Context, j *Job, h, path string) (bool, error) {
	body, _, err := p.c.Objects.Read(ctx, j.Staging+"upstream/"+h+".narinfo", cache.MaxNarInfo)
	switch {
	case errors.Is(err, cache.ErrNotFound), errors.Is(err, cache.ErrTooLarge):
		return false, nil
	case err != nil:
		return false, err
	}
	ni, err := cache.ParseNarInfo(body)
	if err != nil || ni.StorePath != path {
		return false, nil
	}
	info := ni.Info()
	for _, sig := range ni.Sigs {
		for _, k := range p.c.UpstreamKeys {
			if cache.Verify(k, sig, info) {
				return true, nil
			}
		}
	}
	return false, nil
}
