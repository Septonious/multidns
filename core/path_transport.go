package core

import (
	"context"
	"strconv"
	"time"

	"github.com/miekg/dns"
)

type pathReply struct {
	message        *dns.Msg
	err            error
	done           <-chan struct{}
	elapsed        time.Duration
	network        string
	udpUnavailable bool
	fallback       bool
	udpCanary      bool
	started        time.Time
}

// Choose the transport for one authenticated RPC. TCP preference is learned
// only after the caller validates the tunnel reply, never from an arbitrary
// DNS answer. TCP connections use the same protection, cancellation and reuse.
func (c *Client) exchangePathDNS(ctx context.Context, m *dns.Msg, addr string, op byte) pathReply {
	started := time.Now()
	network := "udp"
	canary := false
	c.adaptiveMu.Lock()
	p := c.pathLocked(addr)
	if p.preferTCP {
		network = "tcp"
		if op == opProbe && !time.Now().Before(p.udpProbeAt) {
			network, canary = "udp", true
			p.udpProbeAt = time.Now().Add(10 * time.Second)
		}
	}
	c.adaptiveMu.Unlock()
	attempt := func(network string) pathReply {
		at := time.Now()
		attemptCtx := ctx
		wireNetwork := network
		if network == "tcp" {
			// Idle long-polls on a sequential TCP DNS server cannot hold up
			// upload ACKs and control messages on the other connection.
			if op == opRead {
				wireNetwork = "tcp-read"
			} else if op == opWrite {
				c.adaptiveMu.Lock()
				p.tcpCursor++
				wireNetwork = "tcp-write-" + strconv.Itoa(int(p.tcpCursor%4))
				c.adaptiveMu.Unlock()
			}
			limited, cancel := context.WithTimeout(ctx, c.exchangeTimeout(addr, op))
			defer cancel()
			attemptCtx = limited
		}
		r, err, done := c.exchangeDNSTracked(attemptCtx, m, addr, wireNetwork, op)
		return pathReply{message: r, err: err, done: done, elapsed: time.Since(at), network: network}
	}
	a := attempt(network)
	a.udpCanary = canary
	if network == "tcp" {
		a.udpUnavailable = true
	}
	if network == "udp" && a.err != nil && ctx.Err() == nil && c.ctx.Err() == nil {
		c.adaptiveMu.Lock()
		p.udpFailures++
		fallback := canary || op == opProbe || p.udpFailures >= 2 && time.Since(p.udpSuccess) > 2*time.Second
		if h := c.health[addr]; h != nil {
			if failureKind(a.err) == "timeout" {
				h.UDPTimeouts++
			} else {
				h.UDPSocketErrors++
			}
		}
		c.adaptiveMu.Unlock()
		if fallback {
			a = attempt("tcp")
			a.udpUnavailable, a.fallback = true, true
		}
	}
	if a.err == nil && a.message.Truncated && a.network == "udp" {
		a = attempt("tcp")
		a.fallback = true
	}
	if a.fallback {
		c.adaptiveMu.Lock()
		if h := c.health[addr]; h != nil {
			h.TCPFallbacks++
		}
		c.adaptiveMu.Unlock()
	}
	a.started = started
	return a
}

func (c *Client) recordTransportSuccess(addr string, a pathReply) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	p := c.pathLocked(addr)
	previous := p.preferTCP
	if a.network == "udp" {
		p.udpSuccess, p.udpFailures = time.Now(), 0
		if !previous || a.udpCanary {
			p.preferTCP = false
		}
	} else if a.udpUnavailable && (previous || a.fallback && !p.udpSuccess.After(a.started)) {
		p.preferTCP = true
		if !previous {
			p.udpProbeAt = time.Now().Add(10 * time.Second)
		}
	}
	if previous != p.preferTCP {
		p.txRTT, p.txVariance, p.minRTT = 0, 0, 0
		p.txChecked = time.Time{}
		p.nextSend = time.Time{}
		p.limit = min(p.limit, 8)
		p.rx = readControl{limit: min(max(2, p.rx.limit), 8)}
		if h := c.health[addr]; h != nil {
			h.TransportSwitches++
		}
		c.wakeAdmissionsLocked()
	}
}
