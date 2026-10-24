package diag

func New(code int) {
	if code < 0 {
		panic("unknown code") // allowlisted in allow.txt
	}
}

func Other() {
	panic("x") // want `panic outside package main`
}
