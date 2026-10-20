package core

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type pendingDNS struct {
	question string
	reply    chan *dns.Msg
}
type resolverSocket struct {
	conn    net.Conn
	mu      sync.Mutex
	next    uint16
	pending map[uint16]pendingDNS
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
	s := &resolverSocket{conn: conn, network: network, pending: map[uint16]pendingDNS{}, done: make(chan struct{}), write: make(chan struct{}, 1)}
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
	go s.readLoop()
	return s, nil
}
func (s *resolverSocket) close() { s.once.Do(func() { close(s.done); s.conn.Close() }) }
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
			return
		}
		if !m.Response || len(m.Question) != 1 {
			continue
		}
		s.mu.Lock()
		p, ok := s.pending[m.Id]
		if ok && p.question == m.Question[0].Name {
			select {
			case p.reply <- m:
			default:
			}
		}
		s.mu.Unlock()
	}
}
func (c *Client) exchangeUDP(ctx context.Context, m *dns.Msg, addr string) (*dns.Msg, error) {
	return c.exchangeDNS(ctx, m, addr, "udp")
}

func (c *Client) exchangeDNS(ctx context.Context, m *dns.Msg, addr, network string) (*dns.Msg, error) {
	s, e := c.socket(ctx, addr, network)
	if e != nil {
		return nil, e
	}
	reply := make(chan *dns.Msg, 1)
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
		return nil, fmt.Errorf("DNS transaction space exhausted")
	}
	s.pending[id] = pendingDNS{m.Question[0].Name, reply}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	m = m.Copy()
	m.Id = id
	select {
	case s.write <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, net.ErrClosed
	case <-s.done:
		return nil, net.ErrClosed
	}
	deadline := time.Now().Add(c.exchangeTimeout(addr, opRead))
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetWriteDeadline(deadline)
	e = (&dns.Conn{Conn: s.conn}).WriteMsg(m)
	<-s.write
	if e != nil {
		s.close()
		return nil, e
	}
	timer := time.NewTimer(c.exchangeTimeout(addr, opRead))
	defer timer.Stop()
	select {
	case answer := <-reply:
		return answer, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, net.ErrClosed
	case <-s.done:
		return nil, net.ErrClosed
	case <-timer.C:
		return nil, context.DeadlineExceeded
	}
}
