package materialise

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// States of an owned directory in bob status (SPEC §02.1, §03.6.6).
const (
	StateFresh   = "fresh"   // as bob wrote it, from the current declarations and sources
	StateStale   = "stale"   // the inputs changed since bob wrote it, or a write was interrupted
	StateMissing = "missing" // defined but never written, or files bob wrote are gone
	StateEdited  = "edited"  // a file was edited, added (foreign) or is tracked by git
	StateOrphan  = "orphan"  // no declaration produces it any more (bob gen removes it)
)

// RootStatus is one owned directory as bob status reports it.
type RootStatus struct {
	Dir    string   `json:"dir"` // "" for a defined root bob has not written yet
	Label  string   `json:"label"`
	State  string   `json:"state"`
	Detail []string `json:"detail,omitempty"`
}

// Status compares the manifest and the working tree with the current declarations without
// writing anything (bob status). defined maps the label of every package that puts an output
// into the working tree to the digest of its inputs (Root.Inputs); a manifest root whose label
// is not in defined is an orphan, a defined label without a root is missing. Files are
// classified by sha256 as Apply classifies them; the content of the next output is not known
// (nothing is built), so a root is fresh when its files are bob's and its recorded inputs equal
// the current ones.
func Status(ctx context.Context, repo string, defined map[string]string) ([]RootStatus, error) {
	m, err := readManifest(repo)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(m.Roots))
	for d := range m.Roots {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	tracked := gitTracked(ctx, repo, dirs)
	seen := map[string]bool{}
	var out []RootStatus
	for _, d := range dirs {
		r := m.Roots[d]
		seen[r.Label] = true
		st := RootStatus{Dir: d, Label: r.Label}
		inputs, ok := defined[r.Label]
		if !ok {
			st.State = StateOrphan
			out = append(out, st)
			continue
		}
		edited, missing, temps, err := scanRoot(repo, d, r, tracked)
		if err != nil {
			return nil, err
		}
		switch {
		case len(edited) > 0:
			st.State, st.Detail = StateEdited, edited
		case len(r.Pending) > 0 || len(temps) > 0:
			st.State, st.Detail = StateStale, []string{"a write was interrupted"}
		case len(missing) > 0:
			st.State, st.Detail = StateMissing, missing
		case r.Inputs == "" || r.Inputs != inputs:
			st.State = StateStale
		default:
			st.State = StateFresh
		}
		out = append(out, st)
	}
	var labels []string
	for l := range defined {
		if !seen[l] {
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	for _, l := range labels {
		out = append(out, RootStatus{Label: l, State: StateMissing, Detail: []string{"not generated yet"}})
	}
	return out, nil
}

// scanRoot lists the problems of one owned directory: files that are not bob's (edited,
// foreign, tracked by git), files bob wrote that are gone, and temp files of an interrupted
// write.
func scanRoot(repo, dir string, r ManifestRoot, tracked map[string]bool) (edited, missing, temps []string, err error) {
	abs := filepath.Join(repo, filepath.FromSlash(dir))
	if fi, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
		return nil, []string{dir + " does not exist"}, nil, nil
	} else if err != nil {
		return nil, nil, nil, fmt.Errorf("materialise: %w", err)
	} else if !fi.IsDir() {
		return []string{dir + " is not a directory bob made"}, nil, nil, nil
	}
	disk, temps, err := scanDisk(abs, r.Keep)
	if err != nil {
		return nil, nil, nil, err
	}
	present := map[string]bool{}
	for _, f := range disk {
		present[f.rel] = true
		repoRel := path.Join(dir, f.rel)
		if tracked[repoRel] {
			edited = append(edited, repoRel+" is tracked by git")
			continue
		}
		mf, inFiles := r.Files[f.rel]
		pf, inPending := r.Pending[f.rel]
		sum, err := digest(filepath.Join(abs, filepath.FromSlash(f.rel)), f.info, mf, inFiles)
		if err != nil {
			return nil, nil, nil, err
		}
		switch {
		case inFiles && sum == mf.SHA256, inPending && sum == pf.SHA256:
		case inFiles || inPending:
			edited = append(edited, repoRel+" was edited after bob wrote it")
		default:
			edited = append(edited, repoRel+": bob did not write it")
		}
	}
	for rel := range r.Files {
		if !present[rel] {
			missing = append(missing, path.Join(dir, rel)+" is missing")
		}
	}
	sort.Strings(edited)
	sort.Strings(missing)
	return edited, missing, temps, nil
}

// ManifestLabels lists the labels of the manifest's roots, sorted (none when it cannot be read).
func ManifestLabels(repo string) []string {
	m, err := readManifest(repo)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range m.Roots {
		out = append(out, r.Label)
	}
	sort.Strings(out)
	return out
}
