// Package native wraps a C function.
package native

// #include "add.h"
import "C"

// Add adds two ints in C.
func Add(a, b int) int { return int(C.add(C.int(a), C.int(b))) }
