package main

import (
	"context"
	"flag"
	"github.com/Septonious/multidns/core"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	path := flag.String("profile", "profile.json", "profile JSON")
	addr := flag.String("listen", "127.0.0.1:1080", "SOCKS5 listen address")
	flag.Parse()
	b, e := os.ReadFile(*path)
	if e != nil {
		log.Fatal(e)
	}
	p, e := core.ParseProfile(string(b))
	if e != nil {
		log.Fatal(e)
	}
	c, e := core.NewClient(p, nil)
	if e != nil {
		log.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	e = c.Probe(ctx)
	cancel()
	if e != nil {
		log.Fatal(e)
	}
	l, e := net.Listen("tcp", *addr)
	if e != nil {
		log.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); c.Close() }()
	log.Printf("SOCKS5 ready at %s", *addr)
	if e = c.ServeSOCKS(l); ctx.Err() == nil && e != nil {
		log.Fatal(e)
	}
}
