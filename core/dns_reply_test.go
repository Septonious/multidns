package core

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestCaseNormalizedDNSQuestion(t *testing.T) {
	p, _ := testProfile(t)
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, err := extract(m, p.Domain, p.Key)
		if err != nil {
			return
		}
		f.Data = append([]byte{statusOK}, f.Data...)
		reply := response(m, f, p.Key)
		reply.Question[0].Name = strings.ToUpper(reply.Question[0].Name)
		w.WriteMsg(reply)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Stats().Retries != 0 {
		t.Fatal("valid case-normalized response required retry")
	}
}

func TestHeaderOnlyTruncationFallsBackToAuthenticatedTCP(t *testing.T) {
	p, server := testProfile(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	udp := &dns.Server{PacketConn: packet, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		r := new(dns.Msg)
		r.SetReply(m)
		r.Question = nil
		r.Truncated = true
		w.WriteMsg(r)
	})}
	tcp := &dns.Server{Listener: listener, Handler: server}
	go udp.ActivateAndServe()
	go tcp.ActivateAndServe()
	defer udp.Shutdown()
	defer tcp.Shutdown()
	p.Groups = []Group{{Resolvers: []string{listener.Addr().String()}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Stats().Retries != 0 {
		t.Fatal("truncated reply was dropped instead of using TCP")
	}
}

func TestServerReadSaturationKeepsControlAndBusyReplies(t *testing.T) {
	p, server := testProfile(t)
	for range cap(server.readSlots) {
		server.readSlots <- struct{}{}
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Probe(ctx); err != nil {
		t.Fatal("read saturation blocked control:", err)
	}
	answer, err := c.race(ctx, frame{Op: opRead, Stream: 1}, 0)
	if err != nil || len(answer.Data) != 1 || answer.Data[0] != statusWait {
		t.Fatalf("expected authenticated busy response, got %v %v", answer.Data, err)
	}
	for range cap(server.slots) {
		server.slots <- struct{}{}
	}
	answer, err = c.race(ctx, frame{Op: opProbe, Data: []byte("probe")}, 0)
	if err != nil || len(answer.Data) != 1 || answer.Data[0] != statusWait {
		t.Fatalf("saturated server silently dropped request: %v %v", answer.Data, err)
	}
}

// Failure causes must remain visible after the periodic small health probes.
func TestResolverErrorsSurviveHealthRefresh(t *testing.T) {
	p, server := testProfile(t)
	var mode atomic.Int32
	mode.Store(1)
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if mode.Load() == 1 {
			r := new(dns.Msg)
			r.SetRcode(m, dns.RcodeServerFailure)
			w.WriteMsg(r)
			return
		}
		if mode.Load() == 2 {
			f, err := extract(m, p.Domain, p.Key)
			if err != nil {
				return
			}
			f.Data = append([]byte{statusOK}, f.Data...)
			w.WriteMsg(response(m, f, "wrong-fixture-key"))
			return
		}
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, value := range []int32{1, 2} {
		mode.Store(value)
		if _, err := c.race(ctx, frame{Op: opProbe, Data: []byte("probe")}, 0); err == nil {
			t.Fatal("bad reply accepted")
		}
	}
	mode.Store(0)
	c.TestLatency(ctx, nil)
	stats := c.Stats()
	health := c.healthSnapshot()
	if stats.DNSErrors != 1 || stats.InvalidReplies != 1 || stats.Timeouts != 0 {
		t.Fatalf("incorrect failure categories: %+v", stats)
	}
	if len(health) != 1 || !health[0].Healthy || health[0].DNSErrors != 1 || health[0].InvalidReplies != 1 || health[0].LastError == "" {
		t.Fatalf("health probe erased failure diagnostics: %+v", health)
	}
}
