package main

import (
	"context"
	"flag"
	"github.com/Septonious/multidns/core"
	"github.com/miekg/dns"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	addr := flag.String("listen", ":53", "DNS listen address")
	domain := flag.String("domain", "", "delegated tunnel domain")
	flag.Parse()
	s, e := core.NewServer(*domain, os.Getenv("MULTIDNS_KEY"))
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go s.Cleanup(ctx)
	udp := &dns.Server{Addr: *addr, Net: "udp", Handler: s, UDPSize: 4096}
	tcp := &dns.Server{Addr: *addr, Net: "tcp", Handler: s}
	go func() { <-ctx.Done(); udp.Shutdown(); tcp.Shutdown() }()
	go func() {
		if e := tcp.ListenAndServe(); e != nil {
			log.Print(e)
			cancel()
		}
	}()
	log.Printf("MultiDNS protocol v1 authoritative server on %s for %s", *addr, *domain)
	if e := udp.ListenAndServe(); e != nil {
		log.Fatal(e)
	}
}
