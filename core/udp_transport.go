package core

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type pendingDNS struct {
	question string
	reply    chan *dns.Msg
	done     chan struct{}
	timer    *time.Timer
	err      error
}
type resolverSocket struct {
	conn    net.Conn
	mu      sync.Mutex
	next    uint16
	pending map[uint16]*pendingDNS
	network string
	write   chan struct{}
	done    chan struct{}
	once    sync.Once
}

// ResetSockets preserves session and stream state while rebinding transports.
func (c *Client) ResetSockets() {
	c.socketsMu.Lock()
	defer c.socketsMu.Unlock()
	for _, s := range c.sockets {
		s.close()
	}
	c.sockets = map[string]*resolverSocket{}
	c.socketsEpoch++
	c.networkChanges.Add(1)
}

func (c *Client) socket(ctx context.Context, addr, network string) (*resolverSocket, error) {
	key := network + "|" + addr
	c.socketsMu.Lock()
	if s := c.sockets[key]; s != nil {
		select {
		case <-s.done:
			delete(c.sockets, key)
		default:
			c.socketsMu.Unlock()
			return s, nil
		}
	}
	epoch := c.socketsEpoch
	c.socketsMu.Unlock()
	// Never hold the global socket map lock across a TCP dial or Android's
	// socket-protection callback. Other paths and shutdown must stay responsive.
	conn, e := c.dialer.DialContext(ctx, network, addr)
	if e != nil {
		return nil, e
	}
	if udp, ok := conn.(*net.UDPConn); ok {
		_ = udp.SetReadBuffer(1 << 20)
		_ = udp.SetWriteBuffer(1 << 20)
	}
	s := &resolverSocket{conn: conn, network: network, pending: map[uint16]*pendingDNS{}, done: make(chan struct{}), write: make(chan struct{}, 1)}
	c.socketsMu.Lock()
	defer c.socketsMu.Unlock()
	if c.ctx.Err() != nil || epoch != c.socketsEpoch {
		conn.Close()
		return nil, net.ErrClosed
	}
	if previous := c.sockets[key]; previous != nil {
		select {
		case <-previous.done:
		default:
			conn.Close()
			return previous, nil
		}
	}
	c.sockets[key] = s
	c.adaptiveMu.Lock()
	h := c.health[addr]
	if h == nil {
		h = &ResolverHealth{Endpoint: addr, Healthy: true}
		c.health[addr] = h
	}
	h.Connections++
	if c.socketOpened[key] {
		h.Reconnections++
	}
	c.socketOpened[key] = true
	c.adaptiveMu.Unlock()
	go s.readLoop()
	return s, nil
}
func (s *resolverSocket) close() { s.closeWithError(net.ErrClosed) }
func (s *resolverSocket) closeWithError(cause error) {
	s.once.Do(func() {
		close(s.done)
		s.conn.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, p := range s.pending {
			s.finishLocked(id, p, nil, cause)
		}
	})
}
func (s *resolverSocket) finishLocked(id uint16, p *pendingDNS, m *dns.Msg, err error) {
	if s.pending[id] != p {
		return
	}
	delete(s.pending, id)
	if p.timer != nil {
		p.timer.Stop()
	}
	p.err = err
	if m != nil {
		p.reply <- m
	}
	close(p.done)
}
func (s *resolverSocket) readLoop() {
	defer s.close()
	buffer := make([]byte, 4096)
	tcp := &dns.Conn{Conn: s.conn}
	for {
		var m *dns.Msg
		var e error
		if s.network == "tcp" {
			m, e = tcp.ReadMsg()
		} else {
			var n int
			n, e = s.conn.Read(buffer)
			if e == nil {
				m = new(dns.Msg)
				if m.Unpack(buffer[:n]) != nil {
					continue
				}
			}
		}
		if e != nil {
			s.closeWithError(e)
			return
		}
		if !m.Response || len(m.Question) > 1 {
			continue
		}
		s.mu.Lock()
		p, ok := s.pending[m.Id]
		// DNS names are case-insensitive. Some recursors normalize case.
		// A header-only TC reply is permitted to request TCP fallback; only
		// the subsequent fully authenticated response can complete the RPC.
		match := ok && len(m.Question) == 1 && m.Question[0].Qtype == dns.TypeTXT && m.Question[0].Qclass == dns.ClassINET && strings.EqualFold(p.question, m.Question[0].Name)
		if ok && (match || len(m.Question) == 0 && m.Truncated) {
			s.finishLocked(m.Id, p, m, nil)
		}
		s.mu.Unlock()
	}
}
func (c *Client) exchangeUDP(ctx context.Context, m *dns.Msg, addr string) (*dns.Msg, error) {
	return c.exchangeDNS(ctx, m, addr, "udp")
}

var finishedDNS = func() <-chan struct{} { done := make(chan struct{}); close(done); return done }()

func (c *Client) exchangeDNS(ctx context.Context, m *dns.Msg, addr, network string) (*dns.Msg, error) {
	reply, err, _ := c.exchangeDNSTracked(ctx, m, addr, network)
	return reply, err
}

// Cancellation stops waiting for a reply, not the request already being handled
// by a recursor. Retain its transaction and capacity lease until wire completion.
func (c *Client) exchangeDNSTracked(ctx context.Context, m *dns.Msg, addr, network string, operations ...byte) (*dns.Msg, error, <-chan struct{}) {
	s, e := c.socket(ctx, addr, network)
	if e != nil {
		return nil, e, finishedDNS
	}
	p := &pendingDNS{question: m.Question[0].Name, reply: make(chan *dns.Msg, 1), done: make(chan struct{})}
	var id uint16
	found := false
	s.mu.Lock()
	for range 65536 {
		s.next++
		if _, ok := s.pending[s.next]; !ok {
			id = s.next
			found = true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		return nil, fmt.Errorf("DNS transaction space exhausted"), finishedDNS
	}
	select {
	case <-s.done:
		s.mu.Unlock()
		return nil, net.ErrClosed, finishedDNS
	default:
	}
	s.pending[id] = p
	s.mu.Unlock()
	sent := false
	defer func() {
		if !sent {
			s.mu.Lock()
			s.finishLocked(id, p, nil, net.ErrClosed)
			s.mu.Unlock()
		}
	}()
	m = m.Copy()
	if setting := c.Profile.ResolverSettings[addr]; setting.MTU > 0 {
		m.SetEdns0(uint16(setting.MTU), false)
	}
	m.Id = id
	select {
	case s.write <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err(), p.done
	case <-c.ctx.Done():
		return nil, net.ErrClosed, p.done
	case <-s.done:
		return nil, net.ErrClosed, p.done
	}
	operation := byte(opRead)
	if len(operations) > 0 {
		operation = operations[0]
	}
	timeout := c.exchangeTimeout(addr, operation)
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s.mu.Lock()
	p.timer = time.AfterFunc(timeout, func() { s.mu.Lock(); s.finishLocked(id, p, nil, context.DeadlineExceeded); s.mu.Unlock() })
	s.mu.Unlock()
	sent = true
	_ = s.conn.SetWriteDeadline(deadline)
	e = (&dns.Conn{Conn: s.conn}).WriteMsg(m)
	<-s.write
	if e != nil {
		s.closeWithError(e)
		return nil, e, p.done
	}
	select {
	case answer := <-p.reply:
		return answer, nil, p.done
	case <-ctx.Done():
		return nil, ctx.Err(), p.done
	case <-c.ctx.Done():
		return nil, net.ErrClosed, p.done
	case <-p.done:
		select {
		case answer := <-p.reply:
			return answer, nil, p.done
		default:
			return nil, p.err, p.done
		}
	}
}
