package core

import (
	"context"
	"errors"
	"net"
	"time"
)

// Large downstream replies and tiny upload ACKs have different failure modes.
// Read loss must not collapse a working upload window or its pacing clock.
type readControl struct {
	limit, successes, delaySamples                  int
	rtt, variance, minimum                          float64
	checked, baselineAt, lastCut, retryAt, nextSend time.Time
	lossCuts, delayCuts                             int
}

func (c *Client) readLimitLocked(p *pathState) int {
	if p.preferTCP {
		return 1
	}
	return min(32, max(1, p.limit-2), max(2, p.rx.limit))
}

func (c *Client) updateReadCapacityLocked(addr string, p *pathState, size int, err error, elapsed time.Duration, now time.Time) {
	r := &p.rx
	if err != nil {
		var ne net.Error
		var de dnsFailure
		if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) && !(errors.As(err, &de) && de.code == 2) {
			return
		}
		// The retry uses another path immediately. Briefly stop new reads here;
		// uploads remain eligible, and old losses cannot cut every arriving ACK.
		r.retryAt = now.Add(time.Duration(max(25, min(100, r.rtt/2))) * time.Millisecond)
		if now.Sub(r.lastCut) >= max(300*time.Millisecond, time.Duration(2*r.rtt)*time.Millisecond) && !now.Add(-elapsed).Before(r.lastCut) {
			r.limit = max(2, max(2, r.limit)-max(1, (r.limit+3)/4))
			r.lossCuts++
			r.successes = 0
			r.lastCut = now
		}
		return
	}
	if size <= 0 {
		return // idle long polls are not a bandwidth or congestion sample
	}
	// Full-size replies can still contain an intentional 80ms server wait.
	// Give that variation room, but never let variance hide the absolute guard.
	if size >= chunkSize/2 {
		ms := float64(elapsed.Microseconds()) / 1000
		if r.minimum == 0 || ms < r.minimum || now.Sub(r.baselineAt) > 30*time.Second {
			r.minimum, r.baselineAt = ms, now
		}
		if r.rtt == 0 {
			r.rtt = ms
		} else {
			r.variance = .75*r.variance + .25*absFloat(ms-r.rtt)
			r.rtt = .875*r.rtt + .125*ms
		}
		r.checked = now
		threshold := min(c.writeRTTLimit(), r.minimum+max(80, max(float64(c.Profile.LatencyTargetMS), 4*r.variance)))
		if r.rtt > threshold {
			r.delaySamples++
		} else {
			r.delaySamples = 0
		}
		if r.delaySamples >= 4 && now.Sub(r.lastCut) > 300*time.Millisecond && !now.Add(-elapsed).Before(r.lastCut) {
			r.limit = max(2, max(2, r.limit)-max(1, (r.limit+3)/4))
			r.delayCuts++
			r.lastCut = now
			r.successes, r.delaySamples = 0, 0
			p.signal()
			c.wakeAdmissionsLocked()
			return
		}
	}
	if now.Sub(r.lastCut) < 300*time.Millisecond || now.Add(-elapsed).Before(r.lastCut) {
		return
	}
	r.successes++
	if r.successes >= max(2, r.limit) {
		if r.limit < 32 {
			r.limit = max(2, r.limit) + 1
		}
		r.successes = 0
		// Shared total capacity still grows for a download-only session.
		if p.limit < c.pathCeiling(addr) && now.Sub(p.lastCut) >= 200*time.Millisecond {
			p.limit++
		}
		p.signal()
		c.wakeAdmissionsLocked()
	}
}
