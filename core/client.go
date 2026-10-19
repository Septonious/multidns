package core

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type Protector interface{ Protect(fd int) bool }
type Stats struct {
	Queries    uint64 `json:"queries"`
	Replies    uint64 `json:"replies"`
	Retries    uint64 `json:"retries"`
	Uploaded   uint64 `json:"uploaded"`
	Downloaded uint64 `json:"downloaded"`
	Streams    int64  `json:"streams"`
}
type Client struct {
	Profile                                         Profile
	session                                         uint64
	next                                            atomic.Uint32
	cursor                                          atomic.Uint64
	queries, replies, retries, uploaded, downloaded atomic.Uint64
	streams                                         atomic.Int64
	dialer                                          *net.Dialer
	ctx                                             context.Context
	cancel                                          context.CancelFunc
	slots                                           chan struct{}
}

func NewClient(p Profile, protector Protector) (*Client, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{Profile: p, dialer: &net.Dialer{Timeout: 10 * time.Second}, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 128)}
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		cancel()
		return nil, e
	}
	c.session = binary.BigEndian.Uint64(b[:])
	if protector != nil {
		c.dialer.Control = func(_, _ string, raw syscall.RawConn) error {
			var e error
			e2 := raw.Control(func(fd uintptr) {
				if !protector.Protect(int(fd)) {
					e = fmt.Errorf("socket protection failed")
				}
			})
			if e2 != nil {
				return e2
			}
			return e
		}
	}
	return c, nil
}
func (c *Client) Close() { c.cancel() }
func (c *Client) Stats() Stats {
	return Stats{c.queries.Load(), c.replies.Load(), c.retries.Load(), c.uploaded.Load(), c.downloaded.Load(), c.streams.Load()}
}
func (c *Client) StatsJSON() string { b, _ := json.Marshal(c.Stats()); return string(b) }
func freshNonce() uint64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return uint64(time.Now().Unix())<<32 | uint64(binary.BigEndian.Uint32(b[:]))
}
func (c *Client) race(ctx context.Context, f frame, group int) (frame, error) {
	f.Session = c.session
	f.Nonce = freshNonce()
	m, e := query(f, c.Profile)
	if e != nil {
		return frame{}, e
	}
	type result struct {
		f frame
		e error
	}
	targets := c.Profile.Groups[group%len(c.Profile.Groups)].Resolvers
	results := make(chan result, len(targets))
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, addr := range targets {
		go func(addr string) {
			select {
			case c.slots <- struct{}{}:
				defer func() { <-c.slots }()
			case <-raceCtx.Done():
				results <- result{e: raceCtx.Err()}
				return
			}
			dc := &dns.Client{Net: "udp", Timeout: time.Duration(c.Profile.TimeoutMS) * time.Millisecond, Dialer: c.dialer}
			c.queries.Add(1)
			r, _, err := dc.ExchangeContext(raceCtx, m.Copy(), addr)
			if err == nil && r.Truncated {
				dc.Net = "tcp"
				r, _, err = dc.ExchangeContext(raceCtx, m.Copy(), addr)
			}
			var answer frame
			if err == nil {
				answer, err = parseResponse(r, c.Profile.Key)
				if err == nil && (answer.Session != f.Session || answer.Stream != f.Stream || answer.Seq != f.Seq || answer.Nonce != f.Nonce || answer.Op != f.Op || len(answer.Data) == 0) {
					err = fmt.Errorf("mismatched response")
				}
			}
			if err == nil {
				c.replies.Add(1)
			}
			results <- result{answer, err}
		}(addr)
	}
	var last error
	for range targets {
		r := <-results
		if r.e == nil {
			return r.f, nil
		}
		last = r.e
	}
	return frame{}, last
}
func (c *Client) rpc(ctx context.Context, f frame, base int) ([]byte, error) {
	delay := 20 * time.Millisecond
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			c.retries.Add(1)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, net.ErrClosed
		default:
		}
		answer, e := c.race(ctx, f, (base+attempt)%len(c.Profile.Groups))
		if e == nil {
			switch answer.Data[0] {
			case statusOK:
				return answer.Data[1:], nil
			case statusEOF:
				return nil, io.EOF
			case statusError:
				return nil, fmt.Errorf("server rejected operation %d", f.Op)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-c.ctx.Done():
			timer.Stop()
			return nil, net.ErrClosed
		case <-timer.C:
		}
		delay = min(delay*2, 200*time.Millisecond)
	}
}
func (c *Client) Probe(ctx context.Context) error {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	out, e := c.rpc(ctx, frame{Op: opProbe, Data: b}, 0)
	if e == nil && string(out) != string(b) {
		e = fmt.Errorf("probe mismatch")
	}
	return e
}
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portText, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	var port int
	fmt.Sscanf(portText, "%d", &port)
	switch c.Profile.Route(host, port, network) {
	case "block":
		return nil, fmt.Errorf("blocked by routing rule")
	case "direct":
		return c.dialer.DialContext(ctx, network, address)
	}
	if network != "tcp" && network != "udp" {
		return nil, fmt.Errorf("unsupported network")
	}
	id := c.next.Add(1)
	if id == 0 {
		return nil, fmt.Errorf("stream identifiers exhausted")
	}
	base := int(c.cursor.Add(1) % uint64(len(c.Profile.Groups)))
	data := []byte(address)
	if network == "udp" {
		data = []byte("udp:" + address)
	}
	if _, e = c.rpc(ctx, frame{Op: opOpen, Stream: id, Data: data}, base); e != nil {
		return nil, e
	}
	streamCtx, cancel := context.WithCancel(c.ctx)
	s := &Conn{client: c, id: id, base: base, ctx: streamCtx, cancel: cancel}
	s.reads = make(chan readResult, c.Profile.Window)
	s.deadlineMu.Lock()
	s.readDeadline = time.Now().Add(5 * time.Minute)
	s.writeDeadline = s.readDeadline
	s.deadlineMu.Unlock()
	c.streams.Add(1)
	go s.prefetch()
	if network == "udp" {
		return &datagramConn{Conn: s}, nil
	}
	return s, nil
}

type readResult struct {
	b []byte
	e error
}
type Conn struct {
	client                      *Client
	id                          uint32
	base                        int
	ctx                         context.Context
	cancel                      context.CancelFunc
	readMu, writeMu, deadlineMu sync.Mutex
	readDeadline, writeDeadline time.Time
	readSeq                     atomic.Uint32
	writeSeq                    uint32
	reads                       chan readResult
	buffer                      []byte
	closeOnce                   sync.Once
}

func (s *Conn) group(seq uint32) int {
	if s.client.Profile.Mode == "affinity" {
		return s.base
	}
	return (s.base + int(seq)) % len(s.client.Profile.Groups)
}
func (s *Conn) opContext(read bool) (context.Context, context.CancelFunc) {
	s.deadlineMu.Lock()
	d := s.writeDeadline
	if read {
		d = s.readDeadline
	}
	s.deadlineMu.Unlock()
	if d.IsZero() {
		return context.WithCancel(s.ctx)
	}
	return context.WithDeadline(s.ctx, d)
}
func (s *Conn) prefetch() {
	defer close(s.reads)
	for seq := uint32(0); ; {
		n := s.client.Profile.Window
		batch := make([]chan readResult, n)
		for i := 0; i < n; i++ {
			batch[i] = make(chan readResult, 1)
			num := seq + uint32(i)
			go func(ch chan readResult) {
				ctx, cancel := s.opContext(true)
				defer cancel()
				b, e := s.client.rpc(ctx, frame{Op: opRead, Stream: s.id, Seq: num, Ack: s.readSeq.Load()}, s.group(num))
				ch <- readResult{b, e}
			}(batch[i])
		}
		for _, ch := range batch {
			var r readResult
			select {
			case r = <-ch:
			case <-s.ctx.Done():
				return
			}
			select {
			case s.reads <- r:
			case <-s.ctx.Done():
				return
			}
			seq++
			if r.e != nil {
				return
			}
		}
	}
}
func (s *Conn) Read(b []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	if len(s.buffer) == 0 {
		ctx, cancel := s.opContext(true)
		defer cancel()
		select {
		case r, ok := <-s.reads:
			if !ok {
				return 0, io.EOF
			}
			if r.e != nil {
				return 0, r.e
			}
			s.buffer = r.b
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	n := copy(b, s.buffer)
	s.buffer = s.buffer[n:]
	if len(s.buffer) == 0 {
		s.readSeq.Add(1)
	}
	s.client.downloaded.Add(uint64(n))
	return n, nil
}
func (s *Conn) Write(b []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	total := 0
	// Compute capacity from the actual encoded name rather than assuming a short domain.
	capacity := min(80, (240-len(s.client.Profile.Domain))*5/8-headerSize-8)
	if capacity < 8 {
		return 0, fmt.Errorf("domain leaves insufficient upload capacity")
	}
	for len(b) > 0 {
		n := min(s.client.Profile.Window, (len(b)+capacity-1)/capacity)
		results := make([]chan error, n)
		sizes := make([]int, n)
		for i := 0; i < n; i++ {
			size := min(capacity, len(b))
			data := append([]byte(nil), b[:size]...)
			b = b[size:]
			seq := s.writeSeq
			s.writeSeq++
			sizes[i] = size
			results[i] = make(chan error, 1)
			go func(ch chan error) {
				ctx, cancel := s.opContext(false)
				defer cancel()
				_, e := s.client.rpc(ctx, frame{Op: opWrite, Stream: s.id, Seq: seq, Ack: s.readSeq.Load(), Data: data}, s.group(seq))
				ch <- e
			}(results[i])
		}
		for i, ch := range results {
			if e := <-ch; e != nil {
				return total, e
			}
			total += sizes[i]
			s.client.uploaded.Add(uint64(sizes[i]))
		}
	}
	return total, nil
}
func (s *Conn) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	_, e := s.client.rpc(ctx, frame{Op: opHalfClose, Stream: s.id, Seq: s.writeSeq, Ack: s.readSeq.Load()}, s.base)
	return e
}
func (s *Conn) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.client.streams.Add(-1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = s.client.rpc(ctx, frame{Op: opClose, Stream: s.id, Ack: s.readSeq.Load()}, s.base)
		}()
	})
	return nil
}
func (s *Conn) LocalAddr() net.Addr  { return &net.TCPAddr{IP: net.IPv4zero} }
func (s *Conn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }
func (s *Conn) SetDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	s.writeDeadline = t
	s.deadlineMu.Unlock()
	return nil
}
func (s *Conn) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	s.deadlineMu.Unlock()
	return nil
}
func (s *Conn) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.writeDeadline = t
	s.deadlineMu.Unlock()
	return nil
}
