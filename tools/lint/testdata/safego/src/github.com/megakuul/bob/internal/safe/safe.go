package safe

// Go is the one place a go statement is allowed.
func Go(f func()) {
	go func() {
		defer func() { _ = recover() }()
		f()
	}()
}
