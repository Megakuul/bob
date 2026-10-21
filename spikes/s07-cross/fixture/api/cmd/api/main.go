package main

/*
#cgo LDFLAGS: -lfoo -lrcore -ldl -lpthread -lm
#include <stdlib.h>
#include "foo.h"
#include "rcore.h"
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"unsafe"

	"example.com/api/internal/msg"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

func sha256Hex(s string) string {
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

func main() {
	g, _ := errgroup.WithContext(context.Background())
	var digest, nh string
	g.Go(func() error { digest = sha256Hex("bob"); return nil })
	g.Go(func() error { nh = numHex(42); return nil })
	if err := g.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m := msg.New("hello "+nh, digest)
	b, err := proto.Marshal(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("digest=%s numhex=%s proto_bytes=%d\n", digest, nh, len(b))
}
// incr 1792532087348787095
// incr 1792532096758697941
// incr 1792532131888503408
// incr 1792532137130364209
// incr 1792532176747569238
// incr 1792532191984816581
// incr 1792532206668386384
// incr 1792532538693781551
