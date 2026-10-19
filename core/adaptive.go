package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type ResolverHealth struct {
	Endpoint     string    `json:"endpoint"`
	RTTMS        float64   `json:"rtt_ms"`
	Goodput      float64   `json:"goodput_bytes_per_second"`
	UploadRate   float64   `json:"upload_bytes_per_second"`
	DownloadRate float64   `json:"download_bytes_per_second"`
	Failures     int       `json:"failures"`
	Healthy      bool      `json:"healthy"`
	InFlight     int       `json:"in_flight"`
	Checked      time.Time `json:"checked"`
}

func (c *Client) groupsSnapshot() []Group {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	return append([]Group(nil), c.groups...)
}

func (c *Client) raceTargets(g Group, bulk bool) []string {
	if !c.Profile.Adaptive {
		return g.Resolvers
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	best := math.MaxFloat64
	for _, addr := range g.Resolvers {
		if h := c.health[addr]; h != nil && h.Healthy && h.RTTMS > 0 {
			best = math.Min(best, h.RTTMS)
		}
	}
	out := []string{}
	for _, addr := range g.Resolvers {
		h := c.health[addr]
		if h != nil && !h.Healthy {
			continue
		}
		if !bulk && h != nil && h.RTTMS > math.Max(200, best*4) {
			continue
		}
		out = append(out, addr)
	}
	// If every path is currently down, keep recovery possible while health probes run.
	if len(out) == 0 {
		return g.Resolvers
	}
	return out
}
func (c *Client) healthSnapshot() []ResolverHealth {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	out := []ResolverHealth{}
	for _, h := range c.health {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}
func (c *Client) recordStart(addr string) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	h := c.health[addr]
	if h == nil {
		h = &ResolverHealth{Endpoint: addr, Healthy: true}
		c.health[addr] = h
	}
	h.InFlight++
}
func (c *Client) recordEnd(addr string, elapsed time.Duration, size int, op byte, err error, canceled bool) {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	h := c.health[addr]
	if h == nil {
		return
	}
	h.InFlight = max(0, h.InFlight-1)
	if canceled {
		return
	}
	h.Checked = time.Now()
	if err != nil {
		h.Failures++
		if h.Failures >= 3 {
			h.Healthy = false
		}
		return
	}
	h.Healthy = true
	h.Failures = 0
	// A successful idle long-poll includes server wait time, not network RTT.
	if op != opRead || size > 0 {
		rtt := float64(elapsed.Microseconds()) / 1000
		if h.RTTMS == 0 {
			h.RTTMS = rtt
		} else {
			h.RTTMS = .8*h.RTTMS + .2*rtt
		}
	}
	if size > 0 && elapsed > 0 {
		rate := float64(size) / elapsed.Seconds()
		value := &h.UploadRate
		if op == opRead {
			value = &h.DownloadRate
		}
		if *value == 0 {
			*value = rate
		} else {
			*value = .8*(*value) + .2*rate
		}
		h.Goodput = math.Max(h.UploadRate, h.DownloadRate)
	}
}
func (c *Client) chooseGroup(base int, bulk bool, operation ...byte) int {
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	n := len(c.groups)
	if n <= 1 {
		return 0
	}
	if !c.Profile.Adaptive {
		return base % n
	}
	weights := make([]float64, n)
	best := -1
	bestCost := math.MaxFloat64
	sum := 0.0
	for i, g := range c.groups {
		cost := math.MaxFloat64
		rate := 0.0
		for _, addr := range g.Resolvers {
			h := c.health[addr]
			if h != nil && !h.Healthy {
				continue
			}
			rtt := 100.0
			pending := 0
			throughput := 0.0
			if h != nil {
				if h.RTTMS > 0 {
					rtt = h.RTTMS
				}
				pending = h.InFlight
				throughput = h.Goodput
				if len(operation) > 0 && operation[0] == opRead {
					throughput = h.DownloadRate
				} else if len(operation) > 0 && operation[0] == opWrite {
					throughput = h.UploadRate
				}
			}
			candidate := rtt * (1 + float64(pending)/float64(c.Profile.Window))
			cost = math.Min(cost, candidate)
			if throughput == 0 {
				capacity := float64(chunkSize)
				if len(operation) > 0 && operation[0] == opWrite {
					capacity = float64(c.uploadSize)
				}
				throughput = capacity * 1000 / math.Max(rtt, 1)
			}
			rate = math.Max(rate, throughput/(1+float64(pending)/float64(c.Profile.Window)))
		}
		if cost < bestCost {
			bestCost = cost
			best = i
		}
		weights[i] = rate
		sum += rate
	}
	if best < 0 {
		return base % n
	}
	if !bulk {
		return best
	}
	if len(c.deficits) != n {
		c.deficits = make([]float64, n)
	}
	chosen := best
	highest := -math.MaxFloat64
	for i, w := range weights {
		c.deficits[i] += w
		if w > 0 && c.deficits[i] > highest {
			highest = c.deficits[i]
			chosen = i
		}
	}
	c.deficits[chosen] -= sum
	return chosen
}
func (c *Client) measure(ctx context.Context, addr string) ResolverHealth {
	h := ResolverHealth{Endpoint: addr, Checked: time.Now()}
	var total time.Duration
	success := 0
	for attempt := 0; attempt < 2; attempt++ {
		data := make([]byte, 12)
		_, _ = rand.Read(data)
		f := frame{Op: opProbe, Session: c.session, Nonce: freshNonce(), Data: data}
		m, e := query(f, c.Profile)
		if e != nil {
			h.Failures = 3
			return h
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Duration(min(c.Profile.TimeoutMS, 700))*time.Millisecond)
		start := time.Now()
		dc := &dns.Client{Net: "udp", Timeout: 700 * time.Millisecond, Dialer: c.dialer}
		reply, _, e := dc.ExchangeContext(probeCtx, m, addr)
		cancel()
		if e == nil {
			r, err := parseResponse(reply, c.Profile.Key)
			if err != nil || r.Nonce != f.Nonce || r.Session != f.Session || len(r.Data) != len(data)+1 || r.Data[0] != statusOK || string(r.Data[1:]) != string(data) {
				e = fmt.Errorf("invalid tunnel probe")
			}
		}
		if e == nil {
			total += time.Since(start)
			success++
		}
		if ctx.Err() != nil {
			break
		}
	}
	h.Healthy = success > 0
	if success > 0 {
		h.RTTMS = float64(total.Microseconds()) / 1000 / float64(success)
	} else {
		h.Failures = 3
	}
	return h
}

// TestLatency concurrently checks the whole pool and streams completed results.
func (c *Client) TestLatency(ctx context.Context, emit func(ResolverHealth)) {
	pool := c.Profile.ResolverPool()
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range min(16, len(pool)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range jobs {
				h := c.measure(ctx, addr)
				c.adaptiveMu.Lock()
				old := c.health[addr]
				if old != nil {
					h.Goodput = old.Goodput
					h.UploadRate = old.UploadRate
					h.DownloadRate = old.DownloadRate
					h.InFlight = old.InFlight
				}
				c.health[addr] = &h
				c.adaptiveMu.Unlock()
				if emit != nil {
					emit(h)
				}
			}
		}()
	}
loop:
	for _, addr := range pool {
		select {
		case jobs <- addr:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
}
func FormGroups(pool []ResolverHealth, size, count int) ([]Group, []string) {
	if size < 1 || count < 1 {
		return nil, nil
	}
	healthy := []ResolverHealth{}
	for _, h := range pool {
		if h.Healthy {
			healthy = append(healthy, h)
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		if healthy[i].RTTMS == healthy[j].RTTMS {
			return healthy[i].Endpoint < healthy[j].Endpoint
		}
		return healthy[i].RTTMS < healthy[j].RTTMS
	})
	n := min(len(healthy), size*count)
	if n == 0 {
		return nil, nil
	}
	count = min(count, (n+size-1)/size)
	groups := make([]Group, count)
	for i := range groups {
		groups[i].Name = fmt.Sprintf("Auto %d", i+1)
	}
	for i := 0; i < n; i++ {
		groups[i%count].Resolvers = append(groups[i%count].Resolvers, healthy[i].Endpoint)
	}
	spares := []string{}
	for _, h := range healthy[n:] {
		spares = append(spares, h.Endpoint)
	}
	return groups, spares
}
func (c *Client) regroup() bool {
	groups, spares := FormGroups(c.healthSnapshot(), c.Profile.GroupSize, c.Profile.GroupCount)
	if len(groups) == 0 {
		return false
	}
	c.adaptiveMu.Lock()
	c.groups = groups
	c.spares = spares
	c.deficits = nil
	c.adaptiveMu.Unlock()
	return true
}
func (c *Client) maintain() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, 12*time.Second)
			c.TestLatency(ctx, nil)
			cancel()
			if c.Profile.Automatic {
				c.regroup()
			}
		}
	}
}
