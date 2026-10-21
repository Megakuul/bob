// Command api prints a greeting with a digest from libfoo (C++/openssl) and a number
// formatted by rcore (Rust), both reached through cgo.
package main

/*
#include <stdlib.h>
#include "foo.h"
#include "rcore.h"
*/
import "C"

import (
	"fmt"
	"unsafe"

	"example.com/acme/internal/greet"
	"github.com/google/uuid"
)

var version = "unset"

func digest(s string) string {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	var out [65]C.char
	if C.foo_sha256_hex(cs, &out[0]) != 0 {
		return ""
	}
	return C.GoString(&out[0])
}

func numHex(n uint64) string {
	var out [64]C.char
	if C.rcore_num_hex(C.uint64_t(n), &out[0], C.size_t(len(out))) < 0 {
		return ""
	}
	return C.GoString(&out[0])
}

func line() string {
	return fmt.Sprintf("%s digest=%s numhex=%s version=%s", greet.Hello("api"), digest("bob"), numHex(42), version)
}

func main() {
	_ = uuid.NewString()
	fmt.Println(line())
}
