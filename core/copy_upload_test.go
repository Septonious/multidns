package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/proxy"
)

func TestSOCKSStreamingUploadCommitsBeforeHalfClose(t *testing.T) {
	p, server := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 400
	var flushes atomic.Int64
	var attempts sync.Map
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, _ := extract(m, p.Domain, p.Key)
		if f.Op == opFlush {
			flushes.Add(1)
		}
		if f.Op == opWrite && f.Seq%97 == 11 {
			n, _ := attempts.LoadOrStore(f.Seq, &atomic.Int64{})
			if n.(*atomic.Int64).Add(1) == 1 {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, e := target.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(15 * time.Second))
		data, e := io.ReadAll(conn)
		if e != nil {
			received <- nil
			return
		}
		hash := sha256.Sum256(data)
		conn.Write(hash[:])
		received <- data
	}()
	socks, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socks.Close()
	go c.ServeSOCKS(socks)
	dialer, err := proxy.SOCKS5("tcp", socks.Addr().String(), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	data := make([]byte, 160*1024)
	for i := range data {
		data[i] = byte(i*17 + i/97)
	}
	for offset := 0; offset < len(data); {
		n := min(3333, len(data)-offset)
		if err = writeFull(conn, data[offset:offset+n]); err != nil {
			t.Fatal(err)
		}
		offset += n
	}
	if err = conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	digest := make([]byte, 32)
	if _, err = io.ReadFull(conn, digest); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if !bytes.Equal(digest, hash[:]) {
		t.Fatal("destination received corrupt/truncated upload")
	}
	if !bytes.Equal(<-received, data) {
		t.Fatal("destination EOF preceded full upload commit")
	}
	if flushes.Load() != 0 {
		t.Fatal("SOCKS upload inserted per-buffer flush barriers")
	}
}

type readStartedConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *readStartedConn) Read(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(b)
}
func TestStreamingUploadCancellationInterruptsIdleSourceReader(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	src, peer := net.Pipe()
	defer src.Close()
	defer peer.Close()
	watched := &readStartedConn{Conn: src, started: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, e := raw.(*Conn).copyUpload(watched); done <- e }()
	select {
	case <-watched.started:
	case <-ctx.Done():
		t.Fatal("source read never started")
	}
	raw.Close()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancelled copy succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("idle source reader leaked on disconnect")
	}
	if c.activeUploads.Load() != 0 {
		t.Fatal("cancelled upload retained active flow count")
	}
}
