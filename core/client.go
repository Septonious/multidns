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
	Waits      uint64 `json:"waits"`
	Uploaded   uint64 `json:"uploaded"`
	Downloaded uint64 `json:"downloaded"`
	Streams    int64  `json:"streams"`
}
type Client struct {
	socketsMu sync.Mutex
	sockets   map[string]*resolverSocket

	adaptiveMu sync.Mutex
	groups     []Group
	health     map[string]*ResolverHealth
	spares     []string
	deficits   []float64

	namesMu sync.RWMutex
	names   map[string]nameEntry
	proxies sync.Map

	Profile                                                Profile
	session                                                uint64
	next                                                   atomic.Uint32
	cursor                                                 atomic.Uint64
	queries, replies, retries, waits, uploaded, downloaded atomic.Uint64
	streams                                                atomic.Int64
	dialer                                                 *net.Dialer
	ctx                                                    context.Context
	cancel                                                 context.CancelFunc
	slots                                                  chan struct{}
	readSlots                                              chan struct{}
	uploadSize                                             int
}

func NewClient(p Profile, protector Protector) (*Client, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{Profile: p, dialer: &net.Dialer{Timeout: 10 * time.Second}, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 128), names: map[string]nameEntry{}, sockets: map[string]*resolverSocket{}}
	// Idle download polls must leave capacity for uploads and control requests.
	c.readSlots = make(chan struct{}, 96)
	c.uploadSize = uploadCapacity(p)
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
	c.groups = append([]Group(nil), p.Groups...)
	c.health = map[string]*ResolverHealth{}
	if p.Automatic {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
		c.TestLatency(ctx, nil)
		cancel()
		if !c.regroup() {
			c.cancel()
			return nil, fmt.Errorf("no healthy resolvers passed startup checks")
		}
	}
	if p.Adaptive || p.Automatic {
		go c.maintain()
	}
	return c, nil
}
func (c *Client) Close() {
	c.cancel()
	c.socketsMu.Lock()
	for _, socket := range c.sockets {
		socket.close()
	}
	c.socketsMu.Unlock()
	c.proxies.Range(func(key, value any) bool { key.(net.Conn).Close(); return true })
}
func (c *Client) Stats() Stats {
	return Stats{Queries: c.queries.Load(), Replies: c.replies.Load(), Retries: c.retries.Load(), Waits: c.waits.Load(), Uploaded: c.uploaded.Load(), Downloaded: c.downloaded.Load(), Streams: c.streams.Load()}
}
func (c *Client) StatsJSON() string {
	c.adaptiveMu.Lock()
	spares := append([]string(nil), c.spares...)
	c.adaptiveMu.Unlock()
	b, _ := json.Marshal(struct {
		Stats
		Groups    []Group          `json:"groups"`
		Resolvers []ResolverHealth `json:"resolvers"`
		Spares    []string         `json:"spares"`
	}{c.Stats(), c.groupsSnapshot(), c.healthSnapshot(), spares})
	return string(b)
}
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
	groups := c.groupsSnapshot()
	targets := c.raceTargets(groups[group%len(groups)], f.Bulk)
	results := make(chan result, len(targets))
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, addr := range targets {
		go func(addr string) {
			if f.Op == opRead {
				select {
				case c.readSlots <- struct{}{}:
					defer func() { <-c.readSlots }()
				case <-raceCtx.Done():
					results <- result{e: raceCtx.Err()}
					return
				}
			}
			select {
			case c.slots <- struct{}{}:
				defer func() { <-c.slots }()
			case <-raceCtx.Done():
				results <- result{e: raceCtx.Err()}
				return
			}
			started := time.Now()
			c.recordStart(addr)
			dc := &dns.Client{Net: "udp", Timeout: time.Duration(c.Profile.TimeoutMS) * time.Millisecond, Dialer: c.dialer}
			c.queries.Add(1)
			r, err := c.exchangeUDP(raceCtx, m, addr)
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
			size := 0
			if err == nil && answer.Data[0] == statusOK {
				if f.Op == opRead {
					size = len(answer.Data) - 1
				} else if f.Op == opWrite {
					size = len(f.Data)
				}
			}
			c.recordEnd(addr, time.Since(started), size, f.Op, err, raceCtx.Err() != nil)
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
	return c.rpcACK(ctx, f, base, nil)
}

func (c *Client) rpcACK(ctx context.Context, f frame, base int, ack func() uint32) ([]byte, error) {
	delay := 20 * time.Millisecond
	group := base
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, net.ErrClosed
		default:
		}
		if ack != nil {
			f.Ack = ack()
		}
		answer, e := c.race(ctx, f, group%len(c.groupsSnapshot()))
		if e == nil {
			switch answer.Data[0] {
			case statusOK:
				return answer.Data[1:], nil
			case statusEOF:
				return nil, io.EOF
			case statusError:
				return nil, fmt.Errorf("server rejected operation %d", f.Op)
			case statusWait:
				c.waits.Add(1)
			}
		} else {
			if ctx.Err() != nil || c.ctx.Err() != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, net.ErrClosed
			}
			c.retries.Add(1)
			group++
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
	switch c.route(host, port, network) {
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
	base := int(c.cursor.Add(1) % uint64(len(c.groupsSnapshot())))
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
	traffic atomic.Uint64

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

func (s *Conn) group(seq uint32, op byte) int {
	base := s.base
	if s.client.Profile.Mode != "affinity" {
		base += int(seq)
	}
	return s.client.chooseGroup(base, s.traffic.Load() >= 16*1024, op)
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
	type completion struct {
		seq uint32
		readResult
	}
	window := uint32(s.client.Profile.Window)
	results := make(chan completion, window)
	pending := make(map[uint32]readResult, window)
	next, delivered := uint32(0), uint32(0)
	launch := func(seq uint32) {
		go func() {
			ctx, cancel := s.opContext(true)
			defer cancel()
			b, err := s.client.rpcACK(ctx, frame{Op: opRead, Stream: s.id, Seq: seq, Bulk: s.traffic.Load() >= 16*1024}, s.group(seq, opRead), s.readSeq.Load)
			results <- completion{seq, readResult{b, err}}
		}()
	}
	for {
		for next-delivered < window {
			launch(next)
			next++
		}
		select {
		case result := <-results:
			pending[result.seq] = result.readResult
		case <-s.ctx.Done():
			return
		}
		for {
			result, ok := pending[delivered]
			if !ok {
				break
			}
			select {
			case s.reads <- result:
			case <-s.ctx.Done():
				return
			}
			delete(pending, delivered)
			delivered++
			if result.e != nil {
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
	s.traffic.Add(uint64(n))
	return n, nil
}
func (s *Conn) Write(b []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	capacity := s.client.uploadSize
	if capacity < 8 {
		return 0, fmt.Errorf("domain leaves insufficient upload capacity")
	}
	ctx, cancel := s.opContext(false)
	defer cancel()
	type completion struct {
		index, size int
		err         error
	}
	window := s.client.Profile.Window
	results := make(chan completion, window)
	pending := make(map[int]completion, window)
	next, acknowledged, offset, total := 0, 0, 0, 0
	for offset < len(b) || acknowledged < next {
		for offset < len(b) && next-acknowledged < window {
			size := min(capacity, len(b)-offset)
			data := append([]byte(nil), b[offset:offset+size]...)
			index, seq := next, s.writeSeq
			s.writeSeq++
			offset += size
			next++
			go func() {
				_, err := s.client.rpcACK(ctx, frame{Op: opWrite, Stream: s.id, Seq: seq, Data: data, Bulk: s.traffic.Load() >= 16*1024}, s.group(seq, opWrite), s.readSeq.Load)
				results <- completion{index, size, err}
			}()
		}
		select {
		case result := <-results:
			pending[result.index] = result
		case <-ctx.Done():
			return total, ctx.Err()
		}
		for {
			result, ok := pending[acknowledged]
			if !ok {
				break
			}
			if result.err != nil {
				return total, result.err
			}
			delete(pending, acknowledged)
			acknowledged++
			total += result.size
			s.client.uploaded.Add(uint64(result.size))
			s.traffic.Add(uint64(result.size))
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
