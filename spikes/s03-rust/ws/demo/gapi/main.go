package main

// #include <stdlib.h>
// #include "rcore.h"
import "C"

import (
	"fmt"
	"unsafe"
)

func main() {
	buf := (*C.char)(C.malloc(64))
	defer C.free(unsafe.Pointer(buf))
	n := C.rcore_num_hex(1234, buf, 64)
	fmt.Printf("rcore_num_hex(1234)=%s (%d bytes)\n", C.GoString(buf), int(n))
}
