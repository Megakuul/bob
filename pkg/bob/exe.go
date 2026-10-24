package bob

// exe is a Tree used as an Executable (Exe).
type exe struct {
	Node
	main string
}

func (e *exe) MainProgram() string { return e.main }

// Exe returns the package t as an Executable whose main program is mainProgram (a path relative
// to its output), e.g. a script a bob.Run step writes:
//
//	bob.Exe(step, "bin/deploy")
//
// It is the same graph node as t.
func Exe(t Tree, mainProgram string) Executable {
	if isNil(t) {
		nilInput("bob.Exe: the tree is nil")
		return nil
	}
	return &exe{Node: *t.bobNode(), main: mainProgram}
}
