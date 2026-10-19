package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func testDNS(t testing.TB, handler dns.Handler) string {
	t.Helper()
	pc, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	s := &dns.Server{PacketConn: pc, Handler: handler}
	go s.ActivateAndServe()
	t.Cleanup(func() { s.Shutdown() })
	return pc.LocalAddr().String()
}
func testProfile(t testing.TB) (Profile, *Server) {
	t.Helper()
	s, e := NewServer("t.test", "0123456789abcdef0123456789abcdef")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Cleanup(ctx)
	t.Cleanup(cancel)
	a := testDNS(t, s)
	b := testDNS(t, s)
	p := Profile{Domain: s.Domain, Key: s.Key, Groups: []Group{{Name: "race", Resolvers: []string{a, b}}}, TimeoutMS: 150, Window: 8}
	if e = p.Validate(); e != nil {
		t.Fatal(e)
	}
	return p, s
}
func echoTCP(t testing.TB) string {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}
func TestRacingStripedTransfer(t *testing.T) {
	p, _ := testProfile(t)
	dead := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {}))
	p.Groups = append(p.Groups, Group{Name: "dead", Resolvers: []string{dead}})
	c, e := NewClient(p, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e = c.Probe(ctx); e != nil {
		t.Fatal(e)
	}
	conn, e := c.DialContext(ctx, "tcp", echoTCP(t))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	data := bytes.Repeat([]byte("multipath-0123456789"), 512)
	errs := make(chan error, 1)
	go func() { _, e := conn.Write(data); errs <- e }()
	out := make([]byte, len(data))
	if _, e = io.ReadFull(conn, out); e != nil {
		t.Fatal(e)
	}
	if e = <-errs; e != nil {
		t.Fatal(e)
	}
	if sha256.Sum256(data) != sha256.Sum256(out) {
		t.Fatal("payload corrupted or duplicated")
	}
	if c.Stats().Retries == 0 {
		t.Fatal("failed path did not exercise retry")
	}
}
func TestUploadAndPollIdempotency(t *testing.T) {
	p, s := testProfile(t)
	target := echoTCP(t)
	f := frame{Op: opOpen, Session: 42, Stream: 1, Data: []byte(target)}
	for s.handle(f)[0] == statusWait {
		time.Sleep(time.Millisecond)
	}
	f.Op = opWrite
	f.Seq = 1
	f.Data = []byte("world")
	if s.handle(f)[0] != statusOK {
		t.Fatal("out of order write failed")
	}
	f.Seq = 0
	f.Data = []byte("hello ")
	for i := 0; i < 4; i++ {
		if s.handle(f)[0] != statusOK {
			t.Fatal("duplicate write rejected")
		}
	}
	f.Op = opRead
	f.Seq = 0
	f.Data = nil
	var first []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		first = s.handle(f)
		if first[0] == statusOK {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if first[0] != statusOK {
		t.Fatal("no download")
	}
	for range 5 {
		if !bytes.Equal(first, s.handle(f)) {
			t.Fatal("duplicate poll consumed a different chunk")
		}
	}
	_ = p
}
func TestUDPDatagramBoundary(t *testing.T) {
	p, _ := testProfile(t)
	udp, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 65535)
		for {
			n, a, e := udp.ReadFrom(b)
			if e != nil {
				return
			}
			udp.WriteTo(b[:n], a)
		}
	}()
	c, _ := NewClient(p, nil)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, e := c.DialContext(ctx, "udp", udp.LocalAddr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	for _, n := range []int{1, 700, 1500} {
		data := bytes.Repeat([]byte{byte(n)}, n)
		if _, e = conn.Write(data); e != nil {
			t.Fatal(e)
		}
		out := make([]byte, 65535)
		size, e := conn.Read(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(data, out[:size]) {
			t.Fatalf("UDP boundary corrupted: %d -> %d", n, size)
		}
	}
}
func TestConcurrentStreams(t *testing.T) {
	p, _ := testProfile(t)
	c, _ := NewClient(p, nil)
	defer c.Close()
	target := echoTCP(t)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, e := c.DialContext(ctx, "tcp", target)
			if e != nil {
				t.Error(e)
				return
			}
			defer conn.Close()
			data := bytes.Repeat([]byte{byte(i)}, 700)
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			go conn.Write(data)
			out := make([]byte, len(data))
			if _, e = io.ReadFull(conn, out); e != nil || !bytes.Equal(data, out) {
				t.Errorf("stream corruption: %v", e)
			}
		}()
	}
	wg.Wait()
}
func TestAuthentication(t *testing.T) {
	p, _ := testProfile(t)
	p.Key = "different-key-0123456789"
	c, _ := NewClient(p, nil)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if c.Probe(ctx) == nil {
		t.Fatal("wrong key authenticated")
	}
}
