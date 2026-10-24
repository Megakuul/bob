//go:build arm64 || amd64

package fastadd

//go:noescape
func add(a, b int64) int64
