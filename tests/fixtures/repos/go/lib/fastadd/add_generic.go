//go:build !arm64 && !amd64

package fastadd

func add(a, b int64) int64 { return a + b }
