package core

import "time"

type readBudgetControl struct {
	outcomes                               [256]bool
	pos, count, losses, successes, delayed int
	changed                                time.Time
}

func (c *Client) readBudget() int {
	if n := int(c.rxBudget.Load()); n > 0 {
		return n
	}
	return cap(c.readSlots)
}

// Large TXT replies need far fewer requests per byte than QNAME uploads. Keep
// three quarters of the shared wire credits available to active uploads, while
// preserving a receive floor for responses and allowing full RX-only capacity.
// Existing reads drain naturally; never revoke a physical request's credit.
func (c *Client) readAdmissionBudget() int {
	budget := c.readBudget()
	if c.Profile.Adaptive && c.activeUploads.Load() > 0 {
		budget = min(budget, max(8, cap(c.dataSlots)/4))
	}
	return budget
}

// Expand only a saturated, successfully delivering pool, using the negotiated
// server window. Loss/delay withdraw credits; existing requests drain naturally.
// Physical data credits and individual resolver controllers remain authoritative.
func (c *Client) updateReadBudgetLocked(size int, elapsed time.Duration, failed bool, started, now time.Time, useful bool) {
	if !c.Profile.Adaptive || (!failed && size == 0) {
		return
	}
	r := &c.rxControl
	if r.count == len(r.outcomes) {
		if r.outcomes[r.pos] {
			r.losses--
		}
	} else {
		r.count++
	}
	r.outcomes[r.pos] = failed
	if failed {
		r.losses++
	}
	r.pos = (r.pos + 1) % len(r.outcomes)
	limit := c.readBudget()
	ms := float64(elapsed) / float64(time.Millisecond)
	if !failed && size >= chunkSize/2 {
		if ms > c.writeRTTLimit() {
			r.delayed++
		} else {
			r.delayed = 0
		}
	}
	// Individual path controllers already handle ordinary DNS loss. A second
	// pool-wide cut needs substantial aggregate loss, or sustained excess RTT;
	// otherwise sparse ARQ loss compounds into a permanently collapsed pool.
	congested := failed && r.count == len(r.outcomes) && r.losses*100 >= 8*r.count || r.delayed >= 16
	if congested && now.Sub(r.changed) >= 500*time.Millisecond && !started.Before(r.changed) {
		c.rxBudget.Store(int64(max(32, limit-max(8, limit/8))))
		r.changed, r.successes, r.delayed = now, 0, 0
		c.wakeAdmissionsLocked()
		return
	}
	if !useful || failed || size < chunkSize/2 || ms > c.writeRTTLimit() || started.Before(r.changed) {
		return
	}
	r.successes++
	ceiling := min(cap(c.readSlots), 160)
	if c.peerReadWindow.Load() >= largeReadWindow {
		ceiling = cap(c.readSlots)
	}
	if limit < ceiling && c.leasedReads >= limit*3/4 && r.successes >= max(32, limit/2) && now.Sub(r.changed) >= 200*time.Millisecond && (r.count < 128 || r.losses*200 <= r.count) {
		c.rxBudget.Store(int64(min(ceiling, limit+16)))
		r.changed, r.successes = now, 0
		c.wakeAdmissionsLocked()
	}
}
