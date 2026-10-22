package bop

import (
	"slices"
	"testing"
)

// TestExternalSteps: the commands of each build system (SPEC §03.1.2).
func TestExternalSteps(t *testing.T) {
	a := ExternalArgs{Shell: "/s/bash", Out: "/o", Profile: "debug", Args: []string{"-DX=ON"}}
	for build, want := range map[string][][]string{
		"make": {{"make", "-j2", "SHELL=/s/bash", "PREFIX=/o", "install", "-DX=ON"}},
		"cmake": {
			{"cmake", "-S", ".", "-B", "/b", "-G", "Ninja", "-DCMAKE_INSTALL_PREFIX=/o", "-DCMAKE_BUILD_TYPE=Debug",
				"-DCMAKE_INSTALL_LIBDIR=lib", "-DCMAKE_INSTALL_INCLUDEDIR=include", "-DX=ON"},
			{"cmake", "--build", "/b", "--parallel", "2"},
			{"cmake", "--install", "/b"},
		},
		"meson": {
			{"meson", "setup", "/b", "--prefix=/o", "--libdir=lib", "--buildtype=debug", "-DX=ON"},
			{"ninja", "-C", "/b", "-j2"},
			{"ninja", "-C", "/b", "install"},
		},
	} {
		a.Build = build
		steps, err := externalSteps(a, "/src", "/b", "-j2")
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) != len(want) {
			t.Fatalf("%s: %d steps, want %d", build, len(steps), len(want))
		}
		for i, s := range steps {
			if !slices.Equal(s.argv, want[i]) || s.dir != "/src" {
				t.Errorf("%s step %d: %q in %s, want %q", build, i, s.argv, s.dir, want[i])
			}
		}
	}
	a.Build = "scons"
	if _, err := externalSteps(a, "/src", "/b", "-j2"); err == nil {
		t.Error("an unknown build system was accepted")
	}
}

// TestParallel: every call runs, errors are joined, a panic becomes an error.
func TestParallel(t *testing.T) {
	ran := make([]bool, 5)
	err := parallel(2, 5, func(i int) error {
		ran[i] = true
		if i == 3 {
			panic("boom")
		}
		return nil
	})
	if err == nil || !slices.Equal(ran, []bool{true, true, true, true, true}) {
		t.Errorf("parallel: err %v ran %v", err, ran)
	}
}
