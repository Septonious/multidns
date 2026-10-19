package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/Septonious/multidns/core"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	path := flag.String("profile", "profile.json", "server profile")
	ranges := flag.String("ranges", "", "newline, comma, or space separated IPs/CIDRs/ranges")
	workers := flag.Int("workers", 32, "concurrent endpoints, 1..256")
	timeout := flag.Int("timeout", 1500, "timeout milliseconds")
	flag.Parse()
	b, e := os.ReadFile(*path)
	if e != nil {
		log.Fatal(e)
	}
	p, e := core.ParseProfile(string(b))
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var mu sync.Mutex
	encoder := json.NewEncoder(os.Stdout)
	e = core.Scan(ctx, core.ScanOptions{Ranges: *ranges, Workers: *workers, TimeoutMS: *timeout, Profile: p}, nil, func(r core.ScanResult) { mu.Lock(); defer mu.Unlock(); encoder.Encode(r) })
	if e != nil && ctx.Err() == nil {
		log.Fatal(e)
	}
}
