package engine

import "context"

type storeHomeKey struct{}

// WithStoreHome returns a context that tells ecosystems where bob's store lives on the host
// (the directory below which store path p is <home>/p). Native and Plan code that wants to
// read files of store outputs that already exist (compile_commands.json lists the include
// directories of nixpkgs libraries) maps the paths with it; without it, or when the file is
// not there, they use what they know without reading.
func WithStoreHome(ctx context.Context, home string) context.Context {
	return context.WithValue(ctx, storeHomeKey{}, home)
}

// StoreHome is the home WithStoreHome set ("" = none).
func StoreHome(ctx context.Context) string {
	h, _ := ctx.Value(storeHomeKey{}).(string)
	return h
}
