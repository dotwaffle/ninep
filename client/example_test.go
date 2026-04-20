package client_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

func Example() {
	// Setup a memory filesystem server for the example.
	gen := new(server.QIDGenerator)
	root := memfs.NewDir(gen).
		AddFile("hello.txt", []byte("hello, 9p!"))

	srv := server.New(root)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Serve(ctx, l); err != nil {
			// server closed
		}
	}()

	// 1. Dial the server.
	nc, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = nc.Close() }()

	c, err := client.Dial(ctx, nc)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// 2. Attach to the root.
	f, err := c.Attach(ctx, "nobody", "")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	// 3. Open the file.
	hello, err := c.OpenFile(ctx, "hello.txt", os.O_RDONLY, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = hello.Close() }()

	// 4. Read the file.
	data, err := io.ReadAll(hello)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%s\n", data)

	// Output:
	// hello, 9p!
}
