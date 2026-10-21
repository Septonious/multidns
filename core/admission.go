package core

import (
	"context"
	"errors"
	"math"
	"time"
)

var errPathBusy = errors.New("resolver has no spare capacity")

type admission struct {
	wake    chan struct{}
	stream  uint32
	bulk    bool
	op      byte
	started time.Time
}

// Wake the selected waiter, not every pending request. Broadcasting here made
// a large pool spend most of its CPU repeatedly contending for adaptiveMu.
func (c *Client) wakeAdmissionsLocked() {
	c.admissionChoiceValid = false
	if next := c.nextAdmissionLocked(); next != nil {
		select {
		case next.wake <- struct{}{}:
		default:
		}
	}
}
func (c *Client) pathAvailableLocked(addr string, op byte, bulk bool) bool {
	// Reserve global credits in the fair queue, before assigning a resolver.
	// Otherwise unsent reads can occupy path leases behind the global RX cap.
	if cap(c.dataSlots) > 0 && c.leasedData >= cap(c.dataSlots) {
		return false
	}
	// Leave a small amount of real wire room for interactive traffic rather
	// than placing its next packet behind a complete bulk window.
	if bulk && cap(c.dataSlots) > 32 && c.leasedData >= cap(c.dataSlots)-8 {
		return false
	}
	if op == opRead && cap(c.readSlots) > 0 && c.leasedReads >= cap(c.readSlots) {
		return false
	}
	p := c.pathLocked(addr)
	if time.Now().Before(p.retryAt) {
		return false
	}
	if h := c.health[addr]; h != nil && !h.Healthy {
		if time.Now().Before(p.retryAt) || p.flight > 0 {
			return false
		}
	}
	settings := c.Profile.ResolverSettings[addr]
	// Small windows retain all their credits; larger windows keep one position
	// available for a short foreground request.
	limit := p.limit
	if p.preferTCP {
		limit = min(limit, 5)
	}
	if bulk && limit > 8 {
		limit--
	}
	if p.flight >= limit {
		return false
	}
	if op == opRead {
		if time.Now().Before(p.readRetryAt) || time.Now().Before(p.rx.retryAt) {
			return false
		}
		rx := c.readLimitLocked(p)
		if settings.RXWorkers > 0 {
			rx = min(rx, settings.RXWorkers)
		}
		// A read long-poll carries no upstream data. Reserve half the path for
		// upload whenever writers are waiting, rather than starving their ACKs.
		if c.waitingWrites > 0 {
			rx = min(rx, max(1, p.limit/2))
		}
		return p.reads < rx
	}
	if p.preferTCP && p.flight-p.reads >= 4 {
		return false
	}
	return settings.TXWorkers == 0 || p.flight-p.reads < settings.TXWorkers
}
func (c *Client) leasePathLocked(addr string, op byte, bulk bool) func() {
	p := c.pathLocked(addr)
	c.leasedData++
	p.flight++
	if bulk {
		p.bulk++
	}
	if op == opRead {
		p.reads++
		c.leasedReads++
	}
	return func() {
		c.adaptiveMu.Lock()
		p.flight--
		c.leasedData--
		if bulk {
			p.bulk--
		}
		if op == opRead {
			p.reads--
			c.leasedReads--
		}
		p.signal()
		c.wakeAdmissionsLocked()
		c.adaptiveMu.Unlock()
	}
}
func (c *Client) tryPath(addr string, op byte, streams ...uint32) (func(), error) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if len(c.admissions) > 0 || !c.pathAvailableLocked(addr, op, true) {
		return nil, errPathBusy
	}
	stream := uint32(0)
	if len(streams) > 0 {
		stream = streams[0]
	}
	return c.leaseFlowLocked(addr, op, true, stream), nil
}

// Opportunistic redundancy never jumps ahead of queued unique work, and its
// physical lease is held until the original DNS exchange has actually ended.
func (c *Client) tryAlternative(primary string, op byte, stream uint32) (string, func(), error) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if len(c.admissions) > 0 {
		return "", nil, errPathBusy
	}
	best, cost := "", math.MaxFloat64
	for _, g := range c.groups {
		for _, addr := range g.Resolvers {
			if addr == primary || !c.pathAvailableLocked(addr, op, true) {
				continue
			}
			p := c.pathLocked(addr)
			rtt := 100.0
			if h := c.health[addr]; h != nil {
				if !h.Healthy {
					continue
				}
				if h.RTTMS > 0 {
					rtt = h.RTTMS
				}
			}
			v := rtt * (1 + float64(p.flight)/float64(max(1, p.limit)))
			if v < cost {
				best, cost = addr, v
			}
		}
	}
	if best == "" {
		return "", nil, errPathBusy
	}
	return best, c.leaseFlowLocked(best, op, true, stream), nil
}

// One shared admission queue with per-flow fairness replaces hundreds of requests
// pinned to individual resolvers. A waiting chunk has no resolver ownership:
// removal of any failed path immediately redistributes its waiting work.
func (c *Client) acquireAnyPath(ctx context.Context, preferred []string, op byte, bulk bool, streams ...uint32) (string, func(), error) {
	stream := uint32(0)
	if len(streams) > 0 {
		stream = streams[0]
	}
	a := &admission{wake: make(chan struct{}, 1), stream: stream, bulk: bulk, op: op, started: time.Now()}
	c.adaptiveMu.Lock()
	c.admissions = append(c.admissions, a)
	if op == opWrite {
		c.waitingWrites++
	}
	c.wakeAdmissionsLocked()
	remove := func() {
		for i, v := range c.admissions {
			if v == a {
				c.admissions = append(c.admissions[:i], c.admissions[i+1:]...)
				break
			}
		}
		if op == opWrite {
			c.waitingWrites--
		}
		c.admissionChoiceValid = false
	}
	for {
		if err := ctx.Err(); err != nil {
			remove()
			c.wakeAdmissionsLocked()
			c.adaptiveMu.Unlock()
			return "", nil, err
		}
		if err := c.ctx.Err(); err != nil {
			remove()
			c.wakeAdmissionsLocked()
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
			release := c.leaseFlowLocked(chosen, op, bulk, stream)
			c.wakeAdmissionsLocked()
			if h := c.health[chosen]; h != nil && c.diagnostics.Load() {
				ms := float64(time.Since(a.started).Microseconds()) / 1000
				h.QueueMS = .875*h.QueueMS + .125*ms
				h.MaxQueueMS = max(h.MaxQueueMS, ms)
			}
			c.adaptiveMu.Unlock()
			return chosen, release, nil
		}
		wake := a.wake
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

// Flows with fewer requests on the wire get the next available credit. At equal
// credit counts, aged bulk work wins over fresh interactive work. Blocked reads must
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
	best := int(^uint(0) >> 1)
	for _, a := range c.admissions {
		if !ready(a) {
			continue
		}
		score := 2 * c.flowFlight[flowKey{a.stream, a.op}]
		if a.bulk && now.Sub(a.started) < 50*time.Millisecond {
			score++
		}
		if chosen == nil || score < best {
			chosen = a
			best = score
		}
	}

	c.admissionChoice = chosen
	c.admissionChoiceAt = now
	c.admissionChoiceValid = true
	return chosen
}

func (c *Client) leaseFlowLocked(addr string, op byte, bulk bool, stream uint32) func() {
	release := c.leasePathLocked(addr, op, bulk)
	if c.flowFlight == nil {
		c.flowFlight = map[flowKey]int{}
	}
	key := flowKey{stream, op}
	c.flowFlight[key]++
	p := c.pathLocked(addr)
	if p.streams == nil {
		p.streams = map[uint32]int{}
	}
	p.streams[stream]++
	return func() {
		c.adaptiveMu.Lock()
		c.flowFlight[key]--
		p.streams[stream]--
		if p.streams[stream] == 0 {
			delete(p.streams, stream)
		}
		if c.flowFlight[key] == 0 {
			delete(c.flowFlight, key)
		}
		c.wakeAdmissionsLocked()
		c.adaptiveMu.Unlock()
		release()
	}
}
