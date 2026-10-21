// Package ffi wraps libfoo (C) and rcore (Rust) through cgo.
package ffi

/*
#include <stdlib.h>
#include "foo.h"
#include "rcore.h"
*/
import "C"

import "unsafe"

// Checksum calls foo_checksum from libfoo.so.
func Checksum(s string) uint32 {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return uint32(C.foo_checksum(cs))
}

// NumHex calls rcore_num_hex from the Rust staticlib.
func NumHex(n uint64) string {
	var out [64]C.char
	if C.rcore_num_hex(C.uint64_t(n), &out[0], C.size_t(len(out))) < 0 {
		return ""
	}
	return C.GoString(&out[0])
}
