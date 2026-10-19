package core

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestSlidingWindowsAdvanceBeforeSlowTail(t *testing.T) {
	for _, operation := range []byte{opWrite, opRead} {
		name := "upload"
		if operation == opRead {
			name = "download"
		}
		t.Run(name, func(t *testing.T) {
			p, server := testProfile(t)
			gate, advanced := make(chan struct{}), make(chan struct{})
			var releaseOnce, advancedOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate) }) }
			defer release()
			resolver := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
				if f, err := extract(m, p.Domain, p.Key); err == nil && f.Op == operation {
					if f.Seq == 7 {
						<-gate
					}
					if f.Seq == 8 {
						advancedOnce.Do(func() { close(advanced) })
					}
				}
				server.ServeDNS(w, m)
			}))
			p.Groups = []Group{{Resolvers: []string{resolver}}}
			p.TimeoutMS = 1000
			client, err := NewClient(p, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, "tcp", echoTCP(t))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			payload := bytes.Repeat([]byte("rolling-window"), 2048)
			errors := make(chan error, 2)
			go func() { _, err := conn.Write(payload); errors <- err }()
			go func() {
				out := make([]byte, len(payload))
				_, err := io.ReadFull(conn, out)
				if err == nil && !bytes.Equal(payload, out) {
					err = io.ErrUnexpectedEOF
				}
				errors <- err
			}()
			select {
			case <-advanced:
			case <-time.After(2 * time.Second):
				t.Fatal("next request waited for the slow tail of the previous batch")
			}
			release()
			for range 2 {
				if err := <-errors; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUploadCapacityUsesDNSNameBudget(t *testing.T) {
	for _, domain := range []string{"t.atmosl.ink", strings.Repeat("a", 50) + "." + strings.Repeat("b", 49)} {
		profile := Profile{Domain: domain, Key: strings.Repeat("k", 32)}
		size := uploadCapacity(profile)
		request, err := query(frame{Op: opWrite, Data: bytes.Repeat([]byte{0xff}, size)}, profile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := request.Pack(); err != nil {
			t.Fatal(err)
		}
		if _, err := query(frame{Op: opWrite, Data: make([]byte, size+1)}, profile); err == nil {
			t.Fatal("capacity left usable QNAME space unused")
		}
		if domain == "t.atmosl.ink" && size <= 80 {
			t.Fatal("short-domain uploads still use the old 80-byte cap")
		}
	}
}

func TestDownloadCapacityFitsEDNSBudget(t *testing.T) {
	for _, domain := range []string{"t.atmosl.ink", strings.Repeat("a", 50) + "." + strings.Repeat("b", 49)} {
		p := Profile{Domain: domain, Key: strings.Repeat("k", 32)}
		request, err := query(frame{Op: opRead}, p)
		if err != nil {
			t.Fatal(err)
		}
		size := downloadCapacity(p)
		packet, err := response(request, frame{Op: opRead, Data: make([]byte, size+1)}, p.Key).Pack()
		if err != nil || len(packet) > 1232 {
			t.Fatalf("%s: response exceeds EDNS budget: %d (%v)", domain, len(packet), err)
		}
		if domain == "t.atmosl.ink" && size <= 640 {
			t.Fatal("short-domain downloads still use the old 640-byte cap")
		}
	}
}

func TestWaitRepliesAreNotNetworkRetries(t *testing.T) {
	p, server := testProfile(t)
	var calls atomic.Int32
	resolver := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if calls.Add(1) <= 2 {
			f, err := extract(m, p.Domain, p.Key)
			if err != nil {
				t.Error(err)
				return
			}
			f.Data = []byte{statusWait}
			w.WriteMsg(response(m, f, p.Key))
			return
		}
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{resolver}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := c.Stats(); stats.Waits != 2 || stats.Retries != 0 {
		t.Fatalf("WAIT replies were counted as network failures: %+v", stats)
	}
}

func TestIdleReadsLeaveUploadCapacity(t *testing.T) {
	p, _ := testProfile(t)
	p.Window = 16
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := echoTCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var last io.ReadWriteCloser
	for range 12 {
		conn, err := c.DialContext(ctx, "tcp", target)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		last = conn
	}
	if _, err := last.Write([]byte("upload survives idle polls")); err != nil {
		t.Fatal(err)
	}
	if err := c.Probe(ctx); err != nil {
		t.Fatal("control traffic starved behind idle polls:", err)
	}
}

func TestLongPollWakesDuplicateReaders(t *testing.T) {
	server, err := NewServer("t.test", strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	close(ready)
	stream := &serverStream{ready: ready, notify: make(chan struct{}), chunks: map[uint32][]byte{}}
	server.streams[streamKey{1, 1}] = stream
	results := make(chan []byte, 2)
	for range 2 {
		go func() { results <- server.handle(frame{Op: opRead, Session: 1, Stream: 1}) }()
	}
	time.Sleep(10 * time.Millisecond)
	stream.appendChunk([]byte("immutable"))
	for range 2 {
		select {
		case response := <-results:
			if !bytes.Equal(response, append([]byte{statusOK}, []byte("immutable")...)) {
				t.Fatalf("duplicate poll did not wake with the same chunk: %v", response)
			}
		case <-time.After(time.Second):
			t.Fatal("reader did not wake")
		}
	}
}

func TestLargeReplyTruncatesUDPAndRemainsAvailableOverTCP(t *testing.T) {
	server, err := NewServer("t.test", strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	close(ready)
	payload := bytes.Repeat([]byte{0xab}, 768)
	server.streams[streamKey{1, 1}] = &serverStream{
		ready: ready, notify: make(chan struct{}), nextRead: 1,
		chunks: map[uint32][]byte{0: payload},
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	tcp := &dns.Server{Listener: listener, Handler: server}
	udp := &dns.Server{PacketConn: packet, Handler: server}
	go tcp.ActivateAndServe()
	go udp.ActivateAndServe()
	t.Cleanup(func() { tcp.Shutdown(); udp.Shutdown() })
	profile := Profile{Domain: server.Domain, Key: server.Key}
	request, err := query(frame{Op: opRead, Session: 1, Stream: 1, Nonce: freshNonce()}, profile)
	if err != nil {
		t.Fatal(err)
	}
	request.SetEdns0(900, false)
	client := &dns.Client{Net: "udp", Timeout: time.Second}
	reply, _, err := client.Exchange(request, listener.Addr().String())
	if err != nil || !reply.Truncated {
		t.Fatalf("oversized UDP answer was not truncated: %v (%v)", reply, err)
	}
	if data, err := reply.Pack(); err != nil || len(data) > 900 {
		t.Fatalf("UDP reply exceeded advertised size: %d (%v)", len(data), err)
	}
	client.Net = "tcp"
	reply, _, err = client.Exchange(request, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := parseResponse(reply, profile.Key)
	if err != nil || !bytes.Equal(decoded.Data, append([]byte{statusOK}, payload...)) {
		t.Fatalf("TCP fallback lost the original chunk: %v", err)
	}
}

func TestRollingWindowResumesAfterReaderBackpressure(t *testing.T) {
	p, _ := testProfile(t)
	p.Window = 32
	p.TimeoutMS = 500
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	payload := bytes.Repeat([]byte("bounded queues and refreshed ACKs"), 8192)
	written := make(chan error, 1)
	go func() { _, err := conn.Write(payload); written <- err }()
	// Fill the receive window before consuming it. New polls must refresh ACKs
	// after WAIT replies so a full 64-chunk server queue can make progress.
	time.Sleep(150 * time.Millisecond)
	out := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatal("receive window did not recover from backpressure:", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, out) {
		t.Fatal("rolling window corrupted the stream after backpressure")
	}
}
