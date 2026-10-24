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

	"example.com/api/gen/greetingv1"
	"github.com/google/uuid"
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
	msg := &greetingv1.Greeting{Id: uuid.NewString(), Text: "hello v2 " + nh, Digest: digest}
	b, err := proto.Marshal(msg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("digest=%s numhex=%s proto_bytes=%d\n", digest, nh, len(b))
}
