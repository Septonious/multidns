package core

import (
	"time"
)

// Payload/time from one exchange is a service rate, not aggregate goodput.
// Multiply by usable parallelism before distributing unique bulk work. Keep
// group redundancy as a delayed hedge rather than duplicating every chunk.
func (c *Client) bulkTargets(targets []string, op byte, bulk ...bool) ([]string, time.Duration) {
	if len(targets) < 2 {
		return targets, 0
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if c.pathDeficits == nil {
		c.pathDeficits = map[string]float64{}
	}
	interactive := len(bulk) > 0 && !bulk[0]
	bestCost := 1e300
	chosen := 0
	highest := -1e300
	sum := 0.0
	for i, addr := range targets {
		p := c.pathLocked(addr)
		rtt, rate := 100.0, 0.0
		if h := c.health[addr]; h != nil {
			if h.RTTMS > 0 {
				rtt = h.RTTMS
			}
			rate = h.UploadRate
			if op == opRead {
				rate = h.DownloadRate
			}
		}
		if rate <= 0 {
			size := c.uploadSize
			if op == opRead {
				size = chunkSize
			}
			rate = float64(size) * 1000 / rtt
		}
		capacity := p.limit
		if op == opRead {
			capacity = max(1, capacity-2)
		}
		weight := rate * float64(capacity) / (1 + float64(p.flight+p.waiting)/float64(capacity))
		if interactive {
			cost := rtt * (1 + float64(p.flight+p.waiting)/float64(capacity))
			if cost < bestCost {
				bestCost = cost
				chosen = i
			}
			continue
		}
		sum += weight
		c.pathDeficits[addr] += weight
		if c.pathDeficits[addr] > highest {
			highest = c.pathDeficits[addr]
			chosen = i
		}
	}
	if !interactive {
		c.pathDeficits[targets[chosen]] -= sum
	}
	out := append([]string(nil), targets...)
	out[0], out[chosen] = out[chosen], out[0]
	delay := 100 * time.Millisecond
	if h := c.health[out[0]]; h != nil && h.RTTMS > 0 {
		delay = time.Duration(h.RTTMS+max(h.RTTMS*.5, 4*h.RTTVarianceMS)) * time.Millisecond
	}
	if op == opRead {
		delay += 80 * time.Millisecond
	} else if interactive {
		delay = max(20*time.Millisecond, delay/2)
	}
	return out, min(350*time.Millisecond, max(20*time.Millisecond, delay))
}

func (c *Client) dataCapacity(op byte) int {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	seen := map[string]bool{}
	capacity := 0
	for _, g := range c.groups {
		for _, addr := range g.Resolvers {
			if seen[addr] {
				continue
			}
			seen[addr] = true
			if h := c.health[addr]; h != nil && !h.Healthy {
				continue
			}
			path := c.pathLocked(addr)
			limit := path.limit
			settings := c.Profile.ResolverSettings[addr]
			if op == opRead {
				if time.Now().Before(path.readRetryAt) || time.Now().Before(path.rx.retryAt) {
					continue
				}
				limit = min(c.readLimitLocked(path), max(1, limit-(path.flight-path.reads)))
				if c.activeUploads.Load() > 0 {
					limit = min(limit, max(1, path.limit/2))
				}
				if settings.RXWorkers > 0 {
					limit = min(limit, settings.RXWorkers)
				}
			} else {
				limit = max(1, limit-path.reads)
				if path.preferTCP {
					limit = min(limit, 4)
				}
				if settings.TXWorkers > 0 {
					limit = min(limit, settings.TXWorkers)
				}
			}
			capacity += limit
		}
	}
	return min(cap(c.dataSlots), capacity)
}

// Recompute after shared admission: its chosen primary can be outside the
// originally preferred group, so the old group's RTT is not its hedge timer.
func (c *Client) hedgeDelay(addr string, op byte, bulk bool) time.Duration {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	delay := 100 * time.Millisecond
	if h := c.health[addr]; h != nil && h.RTTMS > 0 {
		delay = time.Duration(h.RTTMS+max(h.RTTMS*.5, 4*h.RTTVarianceMS)) * time.Millisecond
	}
	if op == opRead {
		delay += 80 * time.Millisecond
	} else if !bulk {
		delay = max(20*time.Millisecond, delay/2)
	}
	return min(time.Second, max(20*time.Millisecond, delay))
}
