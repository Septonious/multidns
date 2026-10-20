package core

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestResolverCapacityBackpressure(t *testing.T) {
	p, server := testProfile(t)
	p.Window, p.TimeoutMS, p.Adaptive = 32, 500, true
	p.Groups = []Group{{Name: "capacity"}}
	var dropped atomic.Uint64
	for range 2 {
		slots := make(chan struct{}, 8)
		addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			f, err := extract(m, p.Domain, p.Key)
			if err == nil && (f.Op == opWrite || f.Op == opRead) {
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				default:
					dropped.Add(1)
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
			server.ServeDNS(w, m)
		}))
		p.Groups[0].Resolvers = append(p.Groups[0].Resolvers, addr)
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(18 * time.Second))
	payload := bytes.Repeat([]byte("capacity"), 4096)
	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := conn.Write(payload); done <- err }()
	out := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatal("corrupt payload")
	}
	stats := c.Stats()
	t.Logf("%d bytes in %.3fs; retries=%d timeouts=%d dropped=%d", len(payload), time.Since(start).Seconds(), stats.Retries, stats.Timeouts, dropped.Load())
	// A small amount of capacity discovery loss is expected, sustained overload is not.
	if stats.Retries > 25 {
		t.Fatalf("persistent resolver overload: %d retries", stats.Retries)
	}
}
