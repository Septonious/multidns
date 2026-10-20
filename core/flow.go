package core

import (
	"sort"
	"time"
)

type flowKey struct {
	stream uint32
	op     byte
}

// Control requests use the same healthy low-latency pool regardless of group
// layout. A single-resolver group must not pin flush/handshake to a slow path.
func (c *Client) controlTargets(preferred []string) ([]string, time.Duration) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	candidates := append([]string(nil), preferred...)
	seen := map[string]bool{}
	for _, addr := range candidates {
		seen[addr] = true
	}
	for _, g := range c.groups {
		for _, addr := range g.Resolvers {
			if !seen[addr] {
				candidates = append(candidates, addr)
				seen[addr] = true
			}
		}
	}
	healthy := []string{}
	for _, addr := range candidates {
		if h := c.health[addr]; h == nil || h.Healthy {
			healthy = append(healthy, addr)
		}
	}
	if len(healthy) > 0 {
		candidates = healthy
	}
	cost := func(addr string) float64 {
		rtt := 100.0
		pending := 0
		if h := c.health[addr]; h != nil {
			if h.RTTMS > 0 {
				rtt = h.RTTMS
			}
			pending = h.InFlight
		}
		return rtt * (1 + float64(pending)/float64(max(2, c.pathLocked(addr).limit)))
	}
	sort.SliceStable(candidates, func(i, j int) bool { return cost(candidates[i]) < cost(candidates[j]) })
	candidates = candidates[:min(2, len(candidates))]
	delay := 150 * time.Millisecond
	if len(candidates) > 0 {
		if h := c.health[candidates[0]]; h != nil && h.RTTMS > 0 {
			delay = time.Duration(h.RTTMS+max(h.RTTMS*.5, 4*h.RTTVarianceMS)) * time.Millisecond
		}
	}
	return candidates, min(500*time.Millisecond, max(25*time.Millisecond, delay))
}

func (c *Client) markReader(stream uint32) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if c.activeReaders == nil {
		c.activeReaders = map[uint32]time.Time{}
	}
	c.activeReaders[stream] = time.Now()
}
func (c *Client) readerCount() int {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	now := time.Now()
	count := 0
	for stream, last := range c.activeReaders {
		if now.Sub(last) > time.Second {
			delete(c.activeReaders, stream)
		} else {
			count++
		}
	}
	return max(1, count)
}
func (c *Client) forgetReader(stream uint32) {
	c.adaptiveMu.Lock()
	delete(c.activeReaders, stream)
	c.adaptiveMu.Unlock()
}

// Failed TCP fallback is directional. A tiny health probe or successful upload
// must not immediately put a broken large-reply path back into download use.
func (c *Client) recordTCPFallback(addr string, err error, canceled bool) {
	if !c.Profile.Adaptive || canceled {
		return
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if h := c.health[addr]; h != nil && err != nil {
		h.TCPFallbackFailures++
	}
	p := c.pathLocked(addr)
	if err != nil {
		p.readFailures++
		p.readRetryAt = time.Now().Add(time.Duration(min(30, 2<<min(4, p.readFailures-1))) * time.Second)
	} else {
		p.readFailures = 0
		p.readRetryAt = time.Time{}
	}
	c.wakeAdmissionsLocked()
}
