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
	rx                                      readControl
	preferTCP                               bool
	tcpCursor                               uint32
	udpSuccess, udpProbeAt                  time.Time
	udpFailures, probeFailures              int
	streams                                 map[uint32]int
	lastIssue                               time.Time
	lastSuccess                             time.Time
	failureSince                            time.Time
	lastLoss                                time.Time
	lossBurst                               int
	lastCutMild                             bool
	lastFailure, txChecked                  time.Time
	nextSend                                time.Time
	outcomes                                [128]bool
	outcomePos, outcomeCount, outcomeLosses int
	budgetCuts                              int
	readRetryAt                             time.Time
	readFailures                            int
	lossCuts, delayCuts, recoveries         int
	txRTT, txVariance                       float64
	delaySamples                            int

	bulk                                     int
	retryAt                                  time.Time
	flight, reads, waiting, limit, successes int
	wake                                     chan struct{}
	lastCut, lastIdleGrowth                  time.Time
	minRTT                                   float64
	baselineAt                               time.Time
}

func (c *Client) pathLocked(addr string) *pathState {
	if c.paths == nil {
		c.paths = make(map[string]*pathState)
	}
	p := c.paths[addr]
	if p == nil {
		p = &pathState{limit: min(8, c.Profile.Window), wake: make(chan struct{})}
		p.limit = min(c.pathCeiling(addr), max(2, p.limit))
		p.rx.limit = min(8, max(2, c.Profile.Window))
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
func (c *Client) updateCapacityLocked(addr string, size int, op byte, err error, elapsed time.Duration) {
	if !c.Profile.Adaptive || (op != opRead && op != opWrite) {
		return
	}
	p := c.pathLocked(addr)
	now := time.Now()
	if size <= 0 && err == nil {
		// WAIT is not a congestion sample, but it can cautiously restore the
		// startup floor when real queued work is starved after an old loss cut.
		startup := min(c.pathCeiling(addr), max(2, min(8, c.Profile.Window)))
		if (p.waiting > 0 || len(c.admissions) > 0) && p.limit < startup && now.Sub(p.lastCut) > time.Second && now.Sub(p.lastIdleGrowth) > 500*time.Millisecond {
			p.limit++
			p.recoveries++
			p.lastIdleGrowth = now
			p.signal()
			c.wakeAdmissionsLocked()
		}
		return
	}
	if op == opRead {
		c.updateReadCapacityLocked(addr, p, size, err, elapsed, now)
		return
	}
	p.observeOutcome(err != nil)
	if err != nil {
		// A lost read or one lost write cannot invalidate contemporaneous
		// successful upload ACKs. Expire the clock only when write progress stops.
		if op == opWrite && now.Sub(p.txChecked) > max(100*time.Millisecond, time.Duration(2*p.txRTT*float64(time.Millisecond))) {
			p.lastFailure = now
		}
		var ne net.Error
		var de dnsFailure
		if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) && !(errors.As(err, &de) && de.code == 2) {
			return
		}
		if now.Sub(p.lastLoss) > max(20*time.Millisecond, time.Duration(p.txRTT*float64(time.Millisecond))) {
			p.lossBurst = 0
		}
		p.lossBurst++
		p.lastLoss = now
		// Several losses within one ACK clock reveal a burst before the rolling
		// loss fraction catches up. Escalate a mild cut once, including old packets.
		if p.lastCutMild && p.lossBurst >= 3 {
			p.limit = max(2, p.limit/2)
			p.lastCutMild = false
			p.lossCuts++
			p.successes = 0
			p.lastCut = now
			p.signal()
			c.wakeAdmissionsLocked()
			return
		}
		// Sparse path loss is recoverable ARQ overhead, not proof of queue growth.
		// Unknown/high loss and inflated ACK delay still cause strong backoff.
		mild := p.lossBurst < 3 && p.outcomeCount >= 64 && p.outcomeLosses*100 <= 4*p.outcomeCount && p.txRTT > 0 && p.txRTT < c.writeRTTLimit()
		cooldown := 200 * time.Millisecond
		if mild {
			cooldown = 500 * time.Millisecond
		}
		if now.Sub(p.lastCut) < cooldown || now.Add(-elapsed).Before(p.lastCut) {
			return
		}
		if mild {
			p.limit = max(2, p.limit-max(1, (p.limit+7)/8))
		} else {
			p.limit = max(2, p.limit/2)
		}
		p.lastCutMild = mild
		p.lossCuts++
		p.successes = 0
		p.lastCut = now
		p.signal()
		return
	}
	// Read replies, including nonempty ones, can include the server's deliberate
	// 80 ms wait. They cannot identify network congestion. Use smoothed write ACK
	// timing, sustained samples and a jitter allowance instead of individual reads.
	if op == opWrite {
		if now.Add(-elapsed).After(p.lastFailure) {
			p.txChecked = now
		}
		rtt := float64(elapsed.Microseconds()) / 1000
		if p.minRTT == 0 || rtt < p.minRTT || now.Sub(p.baselineAt) > 30*time.Second {
			p.minRTT = rtt
			p.baselineAt = now
		}
		if p.txRTT == 0 {
			p.txRTT = rtt
		} else {
			p.txVariance = .75*p.txVariance + .25*absFloat(rtt-p.txRTT)
			p.txRTT = .875*p.txRTT + .125*rtt
		}
		// A variance allowance cannot override the absolute loaded-RTT guard.
		hard := c.writeRTTLimit()
		if p.txRTT > hard && now.Sub(p.lastCut) > 200*time.Millisecond && !now.Add(-elapsed).Before(p.lastCut) {
			p.limit = max(2, p.limit/2)
			p.budgetCuts++
			p.lastCutMild = false
			p.lastCut = now
			p.successes = 0
			p.signal()
			c.wakeAdmissionsLocked()
			return
		}
		threshold := min(hard, p.minRTT+max(float64(c.Profile.LatencyTargetMS), 4*p.txVariance))
		if p.txRTT > threshold {
			p.delaySamples++
		} else {
			p.delaySamples = 0
		}
		if p.delaySamples >= 4 && now.Sub(p.lastCut) > max(500*time.Millisecond, time.Duration(4*p.minRTT)*time.Millisecond) && !now.Add(-elapsed).Before(p.lastCut) {
			p.limit = max(2, p.limit-max(1, p.limit/5))
			p.delayCuts++
			p.lastCutMild = false
			p.successes = 0
			p.delaySamples = 0
			p.lastCut = now
			p.signal()
			c.wakeAdmissionsLocked()
			return
		}
	}

	// Replies from requests sent before a cut cannot immediately regrow the
	// reduced budget. Require new evidence after the old burst has drained.
	if now.Sub(p.lastCut) < 200*time.Millisecond || now.Add(-elapsed).Before(p.lastCut) {
		return
	}
	p.successes++
	if p.successes >= p.limit && p.limit < c.pathCeiling(addr) {
		p.limit++
		p.successes = 0
		p.signal()
	}
}

func (c *Client) pathCeiling(addr string) int {
	settings := c.Profile.ResolverSettings[addr]
	if settings.TXWorkers > 0 && settings.RXWorkers > 0 {
		return max(2, max(settings.TXWorkers, settings.RXWorkers))
	}
	return 64
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Keep the two-way application estimate (two DNS RTTs plus server wait) within
// the configured budget. This cannot promise a bound on arbitrary Internet RTT.
func (c *Client) writeRTTLimit() float64 { return max(10, float64(c.Profile.LoadedRTTLimitMS-80)/2) }
func (p *pathState) observeOutcome(failed bool) {
	if p.outcomeCount == len(p.outcomes) {
		if p.outcomes[p.outcomePos] {
			p.outcomeLosses--
		}
	} else {
		p.outcomeCount++
	}
	p.outcomes[p.outcomePos] = failed
	if failed {
		p.outcomeLosses++
	}
	p.outcomePos = (p.outcomePos + 1) % len(p.outcomes)
}
