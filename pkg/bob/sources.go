package bob

import "time"

// Sources is a set of files from the repo, relative to the directory of the calling bob.go.
type Sources struct{ patterns []string }

// Glob selects files by patterns: "*" and "?" do not cross "/", "**" matches any depth,
// "[...]" classes as in path.Match. A pattern starting with "!" excludes.
//
//	bob.Glob("src/**/*.cpp", "!src/legacy/**")
func Glob(patterns ...string) Sources { return Sources{} }

// Files selects exact files (no patterns).
//
//	bob.Files("foo_test.cpp")
func Files(paths ...string) Sources { return Sources{} }

// Dir selects a whole directory subtree.
//
//	bob.Dir("include")
func Dir(path string) Sources { return Sources{} }

// Union combines sources.
func (s Sources) Union(o ...Sources) Sources {
	all := append([]string(nil), s.patterns...)
	for _, x := range o {
		all = append(all, x.patterns...)
	}
	return Sources{patterns: all}
}

// FetchOpts declares one external file or archive.
type FetchOpts struct {
	URL     string   // https (http only with a pinned hash)
	Mirrors []string // tried after URL, in order
	Archive bool     // unpack; false = flat file
	Strip   int      // leading path components to drop (Archive)
	Name    string   // store name; default from the URL's base name
}

// Fetch declares an external input pinned in bob.lock "sources" and fetched by bob, never by
// a build step. Its "out" is the file or the unpacked tree.
func Fetch(p *P, o FetchOpts) Tree { return nil }

// RunOpts declares a hermetic step. Everything the command may read is declared here.
type RunOpts struct {
	Tools   []Tool            // on PATH, in order (nixpkgs.Tool or bob-built tools)
	Srcs    Sources           // copied writable to $BOB_SRC (cwd)
	Inputs  map[string]Tree   // read-only at $BOB_IN_<NAME> (NAME = key upper-cased, [A-Z0-9_])
	Cmd     []string          // argv; no shell. ${VAR} expands from the step's environment only
	Env     map[string]string // extra environment
	Outputs []string          // paths relative to $BOB_OUT that must exist afterwards
	Timeout time.Duration     // 0 = 30 min
}

// Run creates a sandboxed, cached build step. Its output tree is the Step (a Tree).
func Run(p *P, name string, o RunOpts) *Step { return nil }

// Step is the result of Run; it implements Tree.
type Step struct{ Node }

func (s *Step) tree() {}

// File returns one declared output of the step as a Tree.
func (s *Step) File(path string) Tree { return nil }
