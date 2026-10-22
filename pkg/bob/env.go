package bob

import (
	"path"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
)

// frameworkModule is the module of the bob framework.
const frameworkModule = "github.com/megakuul/bob"

// environment is what the evaluator knows about the binary it runs in: the main module, the
// framework version and the modules whose bob.go files were compiled in. It comes from the
// build info (the evaluator is built with -trimpath, so file names are "<module>[@v]/<dir>/…").
type environment struct {
	mainModule string
	bobVersion string
	modules    []module // dependency modules, longest path first
	// isBobFile tells a bob.go file from framework code. Only bob.go files are named bob.go
	// (R-FW-002; the framework's files are named core.go and so on).
	isBobFile func(file string) bool
	// isVarInit tells whether a function name is that of a package's variable initialisers:
	// they run in "<package>.init" (init functions are "<package>.init.0", helpers have their
	// own names).
	isVarInit func(fn string) bool
}

type module struct{ path, version string }

var (
	envOnce sync.Once
	envVal  environment
)

// env returns the environment of this binary (tests replace envVal after a first call).
func env() *environment {
	envOnce.Do(func() { envVal = buildEnv() })
	return &envVal
}

func buildEnv() environment {
	e := environment{
		isBobFile: func(f string) bool { return path.Base(f) == "bob.go" },
		isVarInit: func(fn string) bool { return strings.HasSuffix(fn, ".init") },
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return e
	}
	e.mainModule = bi.Main.Path
	if bi.Main.Path == frameworkModule {
		e.bobVersion = bi.Main.Version
	}
	for _, d := range bi.Deps {
		e.modules = append(e.modules, module{d.Path, d.Version})
		if d.Path == frameworkModule {
			e.bobVersion = d.Version
		}
	}
	sort.Slice(e.modules, func(i, j int) bool { return len(e.modules[i].path) > len(e.modules[j].path) })
	return e
}

// locate splits a compiled file name into the module it belongs to ("" = the main module) and
// the position file: repo-relative for the main module, "<module>@<version>/<dir>/bob.go"
// otherwise (SPEC R-FW-016). dir is the label directory. ok is false for a file of no known
// module.
func (e *environment) locate(file string) (mod, posFile, dir string, ok bool) {
	if e.mainModule != "" {
		if rel, found := strings.CutPrefix(file, e.mainModule+"/"); found {
			return "", rel, labelDir(rel), true
		}
	}
	for _, m := range e.modules {
		for _, prefix := range []string{m.path + "@" + m.version + "/", m.path + "/"} {
			if rel, found := strings.CutPrefix(file, prefix); found {
				return m.path, file, labelDir(rel), true
			}
		}
	}
	return "", file, "", false
}

func labelDir(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

// site is the bob.go position of a call: the first stack frame in a bob.go file.
type site struct {
	mod  string   // "" = main module
	dir  string   // label directory
	pos  Position // module-relative
	ok   bool     // a bob.go frame of a known module was found
	file string   // the compiled file name, for messages when !ok
}

// bobSite returns the first frame in a bob.go file above the caller of bobSite (R-FW-016,
// ruling G8: the first stack frame outside the framework, which names no file bob.go).
func bobSite() site {
	e := env()
	var pcs [64]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if e.isBobFile(f.File) {
			mod, file, dir, ok := e.locate(f.File)
			return site{mod: mod, dir: dir, pos: Position{File: file, Line: f.Line}, ok: ok, file: f.File}
		}
		if !more {
			return site{}
		}
	}
}

// callerFunc returns the file, line and function name of the caller of the function that
// calls callerFunc.
func callerFunc() (file string, line int, fn string) {
	pc, file, line, ok := runtime.Caller(2)
	if !ok {
		return "", 0, ""
	}
	if f := runtime.FuncForPC(pc); f != nil {
		fn = f.Name()
	}
	return file, line, fn
}
