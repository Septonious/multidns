package core

import (
	"context"
	"github.com/miekg/dns"
	"time"
)

// A canceled speculative caller may still receive a valid reply before its
// physical timeout. Authenticate it against the original request before using
// it as progress/delay feedback. It is not delivered application goodput.
func (c *Client) recordLateDNSReply(addr string, op byte, started time.Time, request, reply *dns.Msg) {
	if c.ctx.Err() != nil || reply.Rcode != dns.RcodeSuccess || reply.Truncated {
		return
	}
	want, err := extract(request, c.Profile.Domain, c.Profile.Key)
	if err != nil {
		return
	}
	f, err := parseResponse(reply, c.Profile.Key)
	if err != nil || f.Session != want.Session || f.Stream != want.Stream || f.Seq != want.Seq || f.Op != op || f.Nonce != want.Nonce || len(f.Data) == 0 {
		return
	}
	size := 0
	if f.Data[0] == statusOK {
		if op == opRead {
			size = len(f.Data) - 1
		} else {
			size = len(want.Data)
		}
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	p := c.pathLocked(addr)
	h := c.health[addr]
	if h == nil {
		return
	}
	h.LateReplies++
	c.lateReplies.Add(1)
	if started.Before(p.downAt) {
		return
	}
	now := time.Now()
	wasHealthy := h.Healthy
	c.recoverPathLocked(addr, now)
	elapsed := now.Sub(started)
	c.updateCapacityLocked(addr, size, op, nil, elapsed)
	if op == opRead {
		c.updateReadBudgetLocked(size, elapsed, false, started, now, false)
	}
	if op == opWrite || size > 0 {
		ms := float64(elapsed) / float64(time.Millisecond)
		if h.RTTMS == 0 {
			h.RTTMS = ms
			h.RTTVarianceMS = ms / 2
		} else {
			h.RTTVarianceMS = .75*h.RTTVarianceMS + .25*absFloat(ms-h.RTTMS)
			h.RTTMS = .875*h.RTTMS + .125*ms
		}
	}
	if !wasHealthy {
		c.wakeAdmissionsLocked()
	}
}

func (c *Client) recordAbandonedTimeout(addr string, op byte, started time.Time) {
	if !c.Profile.Adaptive || (op != opRead && op != opWrite) {
		return
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	p := c.pathLocked(addr)
	if started.Before(p.downAt) || started.Before(p.recoveredAt) {
		return
	}
	if h := c.health[addr]; h != nil {
		h.AbandonedTimeouts++
	}
	c.abandonedTimeouts.Add(1)
	now := time.Now()
	c.updateCapacityLocked(addr, 0, op, context.DeadlineExceeded, now.Sub(started))
	if op == opRead {
		c.updateReadBudgetLocked(0, now.Sub(started), true, started, now, false)
	}
}

// Open the circuit once per episode. Late expirations cannot keep extending it
// or restore an overloaded window. Waiting work remains owned by the pool.
func (c *Client) markPathDownLocked(addr string, now time.Time) {
	h := c.health[addr]
	if h == nil {
		return
	}
	p := c.pathLocked(addr)
	if !h.Healthy && !p.downAt.IsZero() && now.Before(p.retryAt) {
		return
	}
	if h.Healthy {
		h.HealthDowns++
	}
	h.Healthy = false
	p.outages = min(5, p.outages+1)
	p.downAt = now
	p.retryAt = now.Add(time.Duration(1<<uint(p.outages-1)) * 500 * time.Millisecond)
	p.limit = min(p.limit, 4)
	p.rx.limit = min(max(2, p.rx.limit), 4)
	p.lastCut, p.rx.lastCut = now, now
	p.successes, p.rx.successes = 0, 0
	p.delaySamples, p.rx.delaySamples = 0, 0
	p.nextSend, p.rx.nextSend = time.Time{}, time.Time{}
	delete(c.dispatchDeficits, addr)
}

func (c *Client) recoverPathLocked(addr string, now time.Time) {
	h := c.health[addr]
	p := c.pathLocked(addr)
	if !h.Healthy {
		h.Recoveries++
		p.limit = min(p.limit, 4)
		p.rx.limit = min(max(2, p.rx.limit), 4)
		p.recoveredAt, p.recoveryUntil = now, now.Add(2*time.Second)
		p.lastCut, p.rx.lastCut = now, now
		p.successes, p.rx.successes = 0, 0
		p.retryAt = time.Time{}
		p.txRTT, p.txVariance, p.minRTT = 0, 0, 0
		p.txChecked = time.Time{}
		p.rx.rtt, p.rx.variance, p.rx.minimum = 0, 0, 0
		p.rx.checked = time.Time{}
		p.outcomes = [128]bool{}
		p.outcomePos, p.outcomeCount, p.outcomeLosses, p.lossBurst = 0, 0, 0, 0
		p.lastCutMild = false
	}
	h.Healthy, h.Failures = true, 0
	p.lastSuccess, p.failureSince = now, time.Time{}
	p.probeFailures = 0
	if !p.recoveredAt.IsZero() && now.Sub(p.recoveredAt) >= 10*time.Second {
		p.outages = 0
	}
}
