// Package fastadd has an assembly implementation on arm64 and amd64.
package fastadd

// Add returns a+b.
func Add(a, b int64) int64 { return add(a, b) }
