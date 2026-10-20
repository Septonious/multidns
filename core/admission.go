package core

import (
	"context"
	"errors"
	"math"
	"time"
)

var errPathBusy = errors.New("resolver has no spare capacity")

type admission struct {
	bulk    bool
	op      byte
	started time.Time
}

func (c *Client) wakeAdmissionsLocked() {
	c.admissionChoiceValid = false
	if c.admissionWake != nil {
		close(c.admissionWake)
	}
	c.admissionWake = make(chan struct{})
}
func (c *Client) pathAvailableLocked(addr string, op byte, bulk bool) bool {
	p := c.pathLocked(addr)
	if h := c.health[addr]; h != nil && !h.Healthy {
		if time.Now().Before(p.retryAt) || p.flight > 0 {
			return false
		}
	}
	settings := c.Profile.ResolverSettings[addr]
	// Priority is bounded in the shared queue, not enforced by a hard bulk
	// reservation that can strand the only remaining direction credit.
	if p.flight >= p.limit {
		return false
	}
	if op == opRead {
		rx := max(1, p.limit-2)
		if settings.RXWorkers > 0 {
			rx = min(rx, settings.RXWorkers)
		}
		// A read long-poll carries no upstream data. Reserve half the path for
		// upload whenever writers are waiting, rather than starving their ACKs.
		for _, a := range c.admissions {
			if a.op == opWrite {
				rx = min(rx, max(1, p.limit/2))
				break
			}
		}
		return p.reads < rx
	}
	return settings.TXWorkers == 0 || p.flight-p.reads < settings.TXWorkers
}
func (c *Client) leasePathLocked(addr string, op byte, bulk bool) func() {
	p := c.pathLocked(addr)
	p.flight++
	if bulk {
		p.bulk++
	}
	if op == opRead {
		p.reads++
	}
	return func() {
		c.adaptiveMu.Lock()
		p.flight--
		if bulk {
			p.bulk--
		}
		if op == opRead {
			p.reads--
		}
		p.signal()
		c.wakeAdmissionsLocked()
		c.adaptiveMu.Unlock()
	}
}
func (c *Client) tryPath(addr string, op byte) (func(), error) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if len(c.admissions) > 0 || !c.pathAvailableLocked(addr, op, true) {
		return nil, errPathBusy
	}
	return c.leasePathLocked(addr, op, true), nil
}

// One shared, FIFO admission queue per priority replaces hundreds of requests
// pinned to individual resolvers. A waiting chunk has no resolver ownership:
// removal of any failed path immediately redistributes its waiting work.
func (c *Client) acquireAnyPath(ctx context.Context, preferred []string, op byte, bulk bool) (string, func(), error) {
	a := &admission{bulk: bulk, op: op, started: time.Now()}
	c.adaptiveMu.Lock()
	c.admissions = append(c.admissions, a)
	c.wakeAdmissionsLocked()
	remove := func() {
		for i, v := range c.admissions {
			if v == a {
				c.admissions = append(c.admissions[:i], c.admissions[i+1:]...)
				break
			}
		}
		c.wakeAdmissionsLocked()
	}
	for {
		if err := ctx.Err(); err != nil {
			remove()
			c.adaptiveMu.Unlock()
			return "", nil, err
		}
		if err := c.ctx.Err(); err != nil {
			remove()
			c.adaptiveMu.Unlock()
			return "", nil, err
		}
		turn := c.nextAdmissionLocked() == a

		chosen := ""
		best := math.MaxFloat64
		sum := 0.0
		if turn {
			candidates := append([]string(nil), preferred...)
			seen := map[string]bool{}
			for _, addr := range candidates {
				seen[addr] = true
			}
			// Affinity is a preference, never a reason to idle available healthy paths.
			for _, group := range c.groups {
				for _, addr := range group.Resolvers {
					if !seen[addr] {
						candidates = append(candidates, addr)
						seen[addr] = true
					}
				}
			}
			for _, addr := range candidates {
				if !c.pathAvailableLocked(addr, op, bulk) {
					continue
				}
				p := c.pathLocked(addr)
				rtt := 100.0
				if h := c.health[addr]; h != nil && h.RTTMS > 0 {
					rtt = h.RTTMS
				}
				cost := rtt * (1 + float64(p.flight)/float64(max(1, p.limit)))
				if bulk {
					if c.dispatchDeficits == nil {
						c.dispatchDeficits = map[string]float64{}
					}
					weight := float64(max(1, p.limit-p.flight)) / rtt
					sum += weight
					c.dispatchDeficits[addr] += weight
					cost = -c.dispatchDeficits[addr]
				}
				if cost < best {
					chosen, best = addr, cost
				}
			}
		}
		if chosen != "" {
			if bulk {
				c.dispatchDeficits[chosen] -= sum
			}
			remove()
			release := c.leasePathLocked(chosen, op, bulk)
			if h := c.health[chosen]; h != nil {
				ms := float64(time.Since(a.started).Microseconds()) / 1000
				h.QueueMS = .875*h.QueueMS + .125*ms
				h.MaxQueueMS = max(h.MaxQueueMS, ms)
			}
			c.adaptiveMu.Unlock()
			return chosen, release, nil
		}
		wake := c.admissionWake
		c.adaptiveMu.Unlock()
		// A short wake also bounds priority aging when no exchange completes.
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-wake:
		case <-ctx.Done():
		case <-c.ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
		c.adaptiveMu.Lock()
	}
}

// A short request can overtake young bulk work, but work waiting 50 ms wins
// over newer arrivals. Only eligible operations participate: blocked reads must
// not prevent a writer from using the reserved TX room.
func (c *Client) nextAdmissionLocked() *admission {
	now := time.Now()
	if c.admissionChoiceValid && now.Sub(c.admissionChoiceAt) < 10*time.Millisecond {
		return c.admissionChoice
	}
	available := map[int]bool{}
	checked := map[int]bool{}
	ready := func(a *admission) bool {
		class := int(a.op) * 2
		if a.bulk {
			class++
		}
		if !checked[class] {
			checked[class] = true
			for _, g := range c.groups {
				for _, addr := range g.Resolvers {
					if c.pathAvailableLocked(addr, a.op, a.bulk) {
						available[class] = true
						return true
					}
				}
			}
		}
		return available[class]
	}
	var chosen *admission
	for _, a := range c.admissions {
		if !ready(a) {
			continue
		}
		if chosen == nil {
			chosen = a
		}
		if now.Sub(chosen.started) >= 50*time.Millisecond || !chosen.bulk {
			break
		}
		if !a.bulk {
			chosen = a
			break
		}
	}
	c.admissionChoice = chosen
	c.admissionChoiceAt = now
	c.admissionChoiceValid = true
	return chosen
}
