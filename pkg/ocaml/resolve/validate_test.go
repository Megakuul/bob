package resolve

import (
	"strings"
	"testing"
)

// validate checks a solution against the opam files directly, without the solver's compiled
// state: every request is met, every chosen version is available, every depends clause has an
// alternative installed at matching versions, no conflict holds and no conflict class is
// shared. It also checks that the solution is closed (no package nobody needs).
func validate(t *testing.T, repo *Repository, arch string, provided map[string]string, reqs []Request, sol *Solution) {
	t.Helper()
	installed := map[string]string{}
	for n, v := range provided {
		installed[n] = v
	}
	for _, p := range sol.Packages {
		if _, ok := installed[p.Name]; ok {
			t.Errorf("%s is twice in the solution (or provided)", p.Name)
		}
		installed[p.Name] = p.Version
	}
	files := map[string]*File{}
	for _, p := range sol.Packages {
		vs, err := repo.Versions(p.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			if v.Version == p.Version {
				files[p.Name] = v.File
			}
		}
		if files[p.Name] == nil {
			t.Errorf("%s.%s is not in the repository", p.Name, p.Version)
		}
	}
	for n, v := range provided {
		vs, _ := repo.Versions(n)
		for _, x := range vs {
			if x.Version == v && x.File != nil {
				files[n] = x.File
			}
		}
	}
	envOf := func(name string) Env {
		return func(v string) (string, bool) {
			switch v {
			case "name":
				return name, true
			case "version":
				return installed[name], true
			}
			if x, ok := globals(arch, provided["ocaml"])[v]; ok {
				return x, true
			}
			if pk, f, ok := strings.Cut(v, ":"); ok && f == "version" && provided[pk] != "" {
				return provided[pk], true
			}
			return "", false
		}
	}
	match := func(a Atom) bool {
		v, ok := installed[a.Name]
		return ok && a.C.Matches(v)
	}
	for _, r := range reqs {
		c, _ := ParseConstraint(r.Constraint)
		if !match(Atom{r.Name, c}) {
			t.Errorf("request %s %s is not met", r.Name, r.Constraint)
		}
	}
	needed := map[string]bool{}
	for _, r := range reqs {
		needed[r.Name] = true
	}
	classes := map[string]string{}
	for name, f := range files {
		env := envOf(name)
		_, isProvided := provided[name]
		for _, it := range Items(f.Fields["available"]) {
			if b, ok := EvalFilter(it, env); !isProvided && (!ok || !b) {
				t.Errorf("%s.%s is not available", name, installed[name])
			}
		}
		if !isProvided {
			deps, err := Deps(f.Fields["depends"], env)
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			for _, cl := range deps {
				ok := false
				for _, alt := range cl {
					all := true
					for _, a := range alt {
						all = all && match(a)
					}
					if all {
						ok = true
						for _, a := range alt {
							needed[a.Name] = true
						}
						break
					}
				}
				if !ok {
					t.Errorf("%s.%s: dependency %s is not met", name, installed[name], clauseString(cl))
				}
			}
		}
		conf, _ := Deps(f.Fields["conflicts"], env)
		for _, a := range conf.Atoms() {
			if match(a) {
				t.Errorf("%s.%s conflicts with %s.%s", name, installed[name], a.Name, installed[a.Name])
			}
		}
		for _, c := range Items(f.Fields["conflict-class"]) {
			if other, ok := classes[c.S]; ok {
				t.Errorf("%s and %s share conflict class %s", name, other, c.S)
			}
			classes[c.S] = name
		}
	}
	for _, p := range sol.Packages {
		if !needed[p.Name] {
			t.Errorf("%s.%s is in the solution but nothing needs it", p.Name, p.Version)
		}
	}
}
