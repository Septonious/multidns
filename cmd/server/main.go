package main

import (
	"context"
	"flag"
	"github.com/Septonious/multidns/core"
	"github.com/miekg/dns"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
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
	packet, e := net.ListenPacket("udp", *addr)
	if e != nil {
		log.Fatal(e)
	}
	if socket, ok := packet.(*net.UDPConn); ok {
		_ = socket.SetReadBuffer(1 << 20)
		_ = socket.SetWriteBuffer(1 << 20)
	}
	udp := &dns.Server{PacketConn: packet, Handler: s, UDPSize: 4096}
	tcp := &dns.Server{Addr: *addr, Net: "tcp", Handler: s, MaxTCPQueries: -1,
		IdleTimeout: func() time.Duration { return 30 * time.Second }}
	go func() { <-ctx.Done(); udp.Shutdown(); tcp.Shutdown() }()
	go func() {
		if e := tcp.ListenAndServe(); e != nil {
			log.Print(e)
			cancel()
		}
	}()
	log.Printf("MultiDNS protocol v1 authoritative server on %s for %s", *addr, *domain)
	if e := udp.ActivateAndServe(); e != nil {
		log.Fatal(e)
	}
}
