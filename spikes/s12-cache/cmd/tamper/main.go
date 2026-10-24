// Command tamper overwrites a staged object with worker credentials (a compromised worker).
package main

import (
	"context"
	"flag"
	"fmt"

	"bobspike/s12/store"

	"github.com/klauspost/compress/zstd"
)

func main() {
	key := flag.String("key", "", "")
	flag.Parse()
	w := store.New("http://127.0.0.1:21203", "workerkey", "workersecret0123456789", "artifacts")
	enc, _ := zstd.NewWriter(nil)
	evil := enc.EncodeAll([]byte("not a nar"), nil)
	fmt.Println("tamper:", *key, w.PutSmall(context.Background(), *key, evil, "", false))
}
