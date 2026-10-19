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
}

func (c *Client) socket(ctx context.Context, addr string) (*resolverSocket, error) {
	c.socketsMu.Lock()
	defer c.socketsMu.Unlock()
	if s := c.sockets[addr]; s != nil {
		select {
		case <-s.done:
			delete(c.sockets, addr)
		default:
			return s, nil
		}
	}
	conn, e := c.dialer.DialContext(ctx, "udp", addr)
	if e != nil {
		return nil, e
	}
	s := &resolverSocket{conn: conn, pending: map[uint16]pendingDNS{}, done: make(chan struct{})}
	c.sockets[addr] = s
	go s.readLoop()
	return s, nil
}
func (s *resolverSocket) close() { s.once.Do(func() { close(s.done); s.conn.Close() }) }
func (s *resolverSocket) readLoop() {
	defer s.close()
	buffer := make([]byte, 4096)
	for {
		n, e := s.conn.Read(buffer)
		if e != nil {
			return
		}
		m := new(dns.Msg)
		if m.Unpack(buffer[:n]) != nil || !m.Response || len(m.Question) != 1 {
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
	s, e := c.socket(ctx, addr)
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
	packet, e := m.Pack()
	if e != nil {
		return nil, e
	}
	if _, e = s.conn.Write(packet); e != nil {
		s.close()
		return nil, e
	}
	timer := time.NewTimer(time.Duration(c.Profile.TimeoutMS) * time.Millisecond)
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
