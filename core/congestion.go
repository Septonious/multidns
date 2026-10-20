package core

import (
	"context"
	"errors"
	"net"
	"time"
)

// All streams share a resolver's capacity. Per-stream windows alone cannot
// prevent ten busy streams from flooding the same recursive resolver.
// Admission happens before global slots and before starting an exchange timer.
type pathState struct {
	flight, reads, waiting, limit, successes int
	wake                                     chan struct{}
	lastCut                                  time.Time
}

func (c *Client) pathLocked(addr string) *pathState {
	if c.paths == nil {
		c.paths = make(map[string]*pathState)
	}
	p := c.paths[addr]
	if p == nil {
		p = &pathState{limit: min(8, c.Profile.Window), wake: make(chan struct{})}
		p.limit = max(2, p.limit)
		c.paths[addr] = p
	}
	return p
}
func (p *pathState) signal() { close(p.wake); p.wake = make(chan struct{}) }
func (c *Client) acquirePath(ctx context.Context, addr string, op byte) (func(), error) {
	if !c.Profile.Adaptive || (op != opRead && op != opWrite) {
		return func() {}, nil
	}
	c.adaptiveMu.Lock()
	p := c.pathLocked(addr)
	p.waiting++
	for {
		if err := ctx.Err(); err != nil {
			p.waiting--
			c.adaptiveMu.Unlock()
			return nil, err
		}
		if err := c.ctx.Err(); err != nil {
			p.waiting--
			c.adaptiveMu.Unlock()
			return nil, net.ErrClosed
		}
		if p.flight < p.limit && (op != opRead || p.reads < max(1, p.limit-2)) {
			p.waiting--
			p.flight++
			if op == opRead {
				p.reads++
			}
			c.adaptiveMu.Unlock()
			return func() {
				c.adaptiveMu.Lock()
				p.flight--
				if op == opRead {
					p.reads--
				}
				p.signal()
				c.adaptiveMu.Unlock()
			}, nil
		}
		wake := p.wake
		c.adaptiveMu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
		case <-c.ctx.Done():
		}
		c.adaptiveMu.Lock()
	}
}
func (c *Client) updateCapacityLocked(addr string, size int, op byte, err error) {
	if !c.Profile.Adaptive || (op != opRead && op != opWrite) {
		return
	}
	p := c.pathLocked(addr)
	now := time.Now()
	if err != nil {
		var ne net.Error
		var de dnsFailure
		if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) && !(errors.As(err, &de) && de.code == 2) {
			return
		}
		// One reduction per loss episode, rather than one per packet in that burst.
		if now.Sub(p.lastCut) < 200*time.Millisecond {
			return
		}
		p.limit = max(2, p.limit/2)
		p.successes = 0
		p.lastCut = now
		p.signal()
		return
	}
	if size <= 0 {
		return
	} // Empty long-polls do not prove bulk capacity.
	p.successes++
	if p.successes >= 2*p.limit && p.limit < 32 {
		p.limit++
		p.successes = 0
		p.signal()
	}
}
