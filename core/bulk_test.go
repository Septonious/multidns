package core

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func bulkFixture(t *testing.T, dropFirst bool) (*Client, *[2]atomic.Int32) {
	t.Helper()
	p, server := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 700
	var requests atomic.Int32
	counts := new([2]atomic.Int32)
	p.Groups = []Group{{Name: "pair"}}
	for i := range 2 {
		addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			f, err := extract(m, p.Domain, p.Key)
			if err == nil && f.Op == opRead {
				counts[i].Add(1)
				if dropFirst && requests.Add(1) == 1 {
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
			server.ServeDNS(w, m)
		}))
		p.Groups[0].Resolvers = append(p.Groups[0].Resolvers, addr)
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c.TestLatency(ctx, nil)
	ready := make(chan struct{})
	close(ready)
	chunks := map[uint32][]byte{}
	for seq := uint32(0); seq < 32; seq++ {
		chunks[seq] = bytes.Repeat([]byte{byte(seq)}, 128)
	}
	server.mu.Lock()
	server.streams[streamKey{c.session, 1}] = &serverStream{ready: ready, notify: make(chan struct{}), chunks: chunks, nextRead: 32}
	server.mu.Unlock()
	return c, counts
}
func TestBulkUsesBothMembersWithoutDuplicatingEveryChunk(t *testing.T) {
	c, counts := bulkFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for seq := uint32(0); seq < 32; seq++ {
		data, err := c.rpc(ctx, frame{Op: opRead, Stream: 1, Seq: seq, Bulk: true}, 0)
		if err != nil || !bytes.Equal(data, bytes.Repeat([]byte{byte(seq)}, 128)) {
			t.Fatalf("chunk %d: %v", seq, err)
		}
	}
	stats := c.Stats()
	if stats.Queries != 32 || stats.Hedges != 0 {
		t.Fatalf("healthy bulk sent duplicate payload requests: %+v", stats)
	}
	if counts[0].Load() < 8 || counts[1].Load() < 8 {
		t.Fatalf("one member was left idle: %d/%d", counts[0].Load(), counts[1].Load())
	}
}
func TestBulkHedgeRecoversDroppedPrimaryBeforeRTO(t *testing.T) {
	c, _ := bulkFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	data, err := c.rpc(ctx, frame{Op: opRead, Stream: 1, Bulk: true}, 0)
	if err != nil || !bytes.Equal(data, bytes.Repeat([]byte{0}, 128)) {
		t.Fatalf("hedge recovery: %v", err)
	}
	stats := c.Stats()
	if stats.Hedges != 1 || stats.Queries != 2 || stats.Retries != 0 {
		t.Fatalf("primary loss did not hedge: %+v", stats)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("hedge waited for full retransmission timeout")
	}
}
func TestExtendedUploadQueueBound(t *testing.T) {
	server, err := NewServer("t.test", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	destination, reader := net.Pipe()
	defer destination.Close()
	defer reader.Close()
	ready := make(chan struct{})
	close(ready)
	stream := &serverStream{conn: destination, ready: ready, notify: make(chan struct{}), writes: map[uint32][]byte{}, chunks: map[uint32][]byte{}, queuedACK: true, writeLimit: largeUploadWindow}
	server.streams[streamKey{1, 1}] = stream
	go stream.flushWrites()
	defer server.handle(frame{Op: opClose, Session: 1, Stream: 1})
	for seq := uint32(0); seq < largeUploadWindow; seq++ {
		if result := server.handle(frame{Op: opWrite, Session: 1, Stream: 1, Seq: seq, Data: []byte{byte(seq)}}); result[0] != statusOK {
			t.Fatalf("negotiated queue rejected position %d", seq)
		}
	}
	if result := server.handle(frame{Op: opWrite, Session: 1, Stream: 1, Seq: largeUploadWindow, Data: []byte{1}}); result[0] != statusWait {
		t.Fatal("extended queue exceeded negotiated bound")
	}
	if result := server.handle(frame{Op: opFlush, Session: 1, Stream: 1, Seq: largeUploadWindow}); result[0] != statusWait {
		t.Fatal("flush acknowledged blocked destination")
	}
	done := make(chan []byte, 1)
	go func() {
		b := make([]byte, largeUploadWindow)
		if _, err := io.ReadFull(reader, b); err != nil {
			done <- nil
			return
		}
		done <- b
	}()
	select {
	case data := <-done:
		if len(data) != largeUploadWindow {
			t.Fatal("extended upload truncated")
		}
		for i, v := range data {
			if v != byte(i) {
				t.Fatal("extended upload reordered")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("extended upload did not drain")
	}
	deadline := time.Now().Add(time.Second)
	for server.handle(frame{Op: opFlush, Session: 1, Stream: 1, Seq: largeUploadWindow})[0] != statusOK {
		if time.Now().After(deadline) {
			t.Fatal("extended upload flush did not finish")
		}
	}
}

func TestCanceledHedgeRetainsWireCreditUntilReply(t *testing.T) {
	p, server := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 700
	entered, gate := make(chan struct{}), make(chan struct{})
	slow := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, e := extract(m, p.Domain, p.Key)
		if e == nil && f.Op == opRead {
			close(entered)
			<-gate
		}
		server.ServeDNS(w, m)
	}))
	fast := testDNS(t, server)
	p.Groups = []Group{{Resolvers: []string{slow, fast}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.health[slow] = &ResolverHealth{Endpoint: slow, Healthy: true, RTTMS: 5}
	c.health[fast] = &ResolverHealth{Endpoint: fast, Healthy: true, RTTMS: 20}
	ready := make(chan struct{})
	close(ready)
	server.mu.Lock()
	server.streams[streamKey{c.session, 1}] = &serverStream{ready: ready, notify: make(chan struct{}), chunks: map[uint32][]byte{0: []byte("wire-credit")}, nextRead: 1}
	server.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := c.rpc(ctx, frame{Op: opRead, Stream: 1}, 0)
	if err != nil || string(out) != "wire-credit" {
		close(gate)
		t.Fatalf("hedged read: %v", err)
	}
	<-entered
	c.adaptiveMu.Lock()
	flight := c.paths[slow].flight
	c.adaptiveMu.Unlock()
	close(gate)
	if flight != 1 {
		t.Fatalf("canceled but still active wire request released its budget: %d", flight)
	}
	deadline := time.Now().Add(time.Second)
	for {
		c.adaptiveMu.Lock()
		flight = c.paths[slow].flight
		c.adaptiveMu.Unlock()
		if flight == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("late reply did not release wire credit")
		}
		time.Sleep(time.Millisecond)
	}
}
