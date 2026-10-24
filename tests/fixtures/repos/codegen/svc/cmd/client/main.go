// Command client calls the server through the generated connect Go client.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"

	"connectrpc.com/connect"

	greetv1 "example.com/svc/gen/proto/greet/v1"
	"example.com/svc/gen/proto/greet/v1/greetv1connect"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:20600", "server base URL")
	flag.Parse()
	c := greetv1connect.NewGreetServiceClient(http.DefaultClient, *url)
	res, err := c.Greet(context.Background(), connect.NewRequest(&greetv1.GreetRequest{Name: "go-client"}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "greet:", err)
		os.Exit(1)
	}
	fmt.Printf("go client: %s (request_id=%s)\n", res.Msg.GetGreeting(), res.Msg.GetMeta().GetRequestId())
	_, err = c.Greet(context.Background(), connect.NewRequest(&greetv1.GreetRequest{}))
	fmt.Printf("go client: empty name -> code=%v\n", connect.CodeOf(err))
}
