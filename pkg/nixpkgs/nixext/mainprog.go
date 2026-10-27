package nixext

import "strings"

// mainProgram returns meta.mainProgram of attr at the surface revision ("" when it has none),
// from the generated table mainPrograms ("\n<attr>\t<program>" lines, sorted). A string
// constant rather than a map: no init cost, and only programs that call Exe link it.
func mainProgram(attr string) string {
	i := strings.Index(mainPrograms, "\n"+attr+"\t")
	if attr == "" || i < 0 {
		return ""
	}
	rest := mainPrograms[i+len(attr)+2:]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}
