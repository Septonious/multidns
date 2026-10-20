package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type ResolverHealth struct {
	AssignedStreams int     `json:"assigned_streams"`
	TXQueries       uint64  `json:"tx_queries"`
	RXQueries       uint64  `json:"rx_queries"`
	TXTimeouts      uint64  `json:"tx_timeouts"`
	RXTimeouts      uint64  `json:"rx_timeouts"`
	HealthDowns     uint64  `json:"health_downs"`
	SlotWaitMS      float64 `json:"slot_wait_ms"`
	MaxSlotWaitMS   float64 `json:"max_slot_wait_ms"`

	PacingWaitMS        float64 `json:"pacing_wait_ms"`
	PacedQueries        uint64  `json:"paced_queries"`
	RecentLossPercent   float64 `json:"recent_loss_percent"`
	CapacityBudgetCuts  int     `json:"capacity_budget_cuts"`
	TCPFallbackFailures uint64  `json:"tcp_fallback_failures"`
	ReadCooldownMS      int64   `json:"read_cooldown_ms"`

	CapacityLossCuts   int `json:"capacity_loss_cuts"`
	CapacityDelayCuts  int `json:"capacity_delay_cuts"`
	CapacityRecoveries int `json:"capacity_recoveries"`

	Queries         uint64  `json:"queries"`
	Replies         uint64  `json:"replies"`
	Retransmissions uint64  `json:"retransmissions"`
	Hedges          uint64  `json:"hedges"`
	Reconnections   uint64  `json:"reconnections"`
	Connections     uint64  `json:"connections"`
	Recoveries      uint64  `json:"recoveries"`
	TCPFallbacks    uint64  `json:"tcp_fallbacks"`
	Uploaded        uint64  `json:"uploaded"`
	Downloaded      uint64  `json:"downloaded"`
	QueueMS         float64 `json:"queue_ms"`
	MaxQueueMS      float64 `json:"max_queue_ms"`
	MinRTTMS        float64 `json:"min_rtt_ms"`
	MTU             int     `json:"mtu"`
	TXWorkers       int     `json:"tx_workers"`
	RXWorkers       int     `json:"rx_workers"`

	Timeouts       uint64    `json:"timeouts"`
	DNSErrors      uint64    `json:"dns_errors"`
	InvalidReplies uint64    `json:"invalid_replies"`
	SocketErrors   uint64    `json:"socket_errors"`
	LastError      string    `json:"last_error,omitempty"`
	Capacity       int       `json:"capacity"`
	WireInFlight   int       `json:"wire_in_flight"`
	Queued         int       `json:"queued"`
	RTTVarianceMS  float64   `json:"rtt_variance_ms"`
	Endpoint       string    `json:"endpoint"`
	RTTMS          float64   `json:"rtt_ms"`
	Goodput        float64   `json:"goodput_bytes_per_second"`
	UploadRate     float64   `json:"upload_bytes_per_second"`
	DownloadRate   float64   `json:"download_bytes_per_second"`
	Failures       int       `json:"failures"`
	Healthy        bool      `json:"healthy"`
	InFlight       int       `json:"in_flight"`
	Checked        time.Time `json:"checked"`
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
		copy := *h
		settings := c.Profile.ResolverSettings[h.Endpoint]
		copy.MTU = settings.MTU
		if copy.MTU == 0 {
			copy.MTU = 1232
		}
		copy.TXWorkers = settings.TXWorkers
		copy.RXWorkers = settings.RXWorkers
		if p := c.paths[h.Endpoint]; p != nil {
			copy.AssignedStreams = len(p.streams)
			copy.CapacityBudgetCuts = p.budgetCuts
			if p.outcomeCount > 0 {
				copy.RecentLossPercent = 100 * float64(p.outcomeLosses) / float64(p.outcomeCount)
			}
			copy.ReadCooldownMS = max(int64(0), time.Until(p.readRetryAt).Milliseconds())
			copy.Capacity = p.limit
			copy.CapacityLossCuts = p.lossCuts
			copy.CapacityDelayCuts = p.delayCuts
			copy.CapacityRecoveries = p.recoveries
			copy.WireInFlight = p.flight
			copy.Queued = p.waiting
		}
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

// A lost packet should not hold an ordered window for the full 1.5 s default
// on a healthy 40 ms path. Keep a jitter/long-poll allowance and the configured
// timeout as a ceiling; unknown or failing paths retain that full timeout.
func (c *Client) exchangeTimeout(addr string, op byte) time.Duration {
	ceiling := time.Duration(c.Profile.TimeoutMS) * time.Millisecond
	if op == opProbe {
		return min(ceiling, 700*time.Millisecond)
	}
	c.adaptiveMu.Lock()
	h := c.health[addr]
	rtt, variance := 0.0, 0.0
	if h != nil && h.Healthy {
		rtt = h.RTTMS
		variance = h.RTTVarianceMS
		if op == opWrite {
			if path := c.paths[addr]; path != nil && path.txRTT > 0 && path.txChecked.After(path.lastFailure) && time.Since(path.txChecked) < 10*time.Second {
				rtt = path.txRTT
				variance = path.txVariance
			}
		}
	}
	c.adaptiveMu.Unlock()
	if !c.Profile.Adaptive || rtt <= 0 {
		return ceiling
	}
	if op == opWrite {
		return min(ceiling, max(160*time.Millisecond, time.Duration(2*rtt+max(25, 4*variance))*time.Millisecond))
	}
	if op == opRead {
		return min(ceiling, max(350*time.Millisecond, time.Duration(2*rtt+max(50, 4*variance)+80)*time.Millisecond))
	}
	allowance := 100 * time.Millisecond
	estimate := max(300*time.Millisecond, time.Duration(max(4*rtt, rtt+4*variance))*time.Millisecond+allowance)
	return min(ceiling, estimate)
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
	h.Queries++
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
	c.updateCapacityLocked(addr, size, op, err, elapsed)
	if err != nil {
		h.Failures++
		h.LastError = failureSummary(err)
		switch failureKind(err) {
		case "timeout":
			h.Timeouts++
			if op == opWrite {
				h.TXTimeouts++
			} else if op == opRead {
				h.RXTimeouts++
			}
		case "dns":
			h.DNSErrors++
		case "invalid":
			h.InvalidReplies++
		default:
			h.SocketErrors++
		}
		// Reachability is not a per-packet loss flag. Other authenticated ACKs
		// prove that a resolver is still alive even while one datagram expires.
		// Three failed exchanges with no recent progress open the circuit;
		// their ARQ retries are already free to use every other live path.
		path := c.pathLocked(addr)
		path.lastIssue = time.Now()
		// Briefly route new work elsewhere without displaying a disconnect.
		// This also stops an overloaded recursor accumulating a fresh burst.
		path.retryAt = time.Now().Add(time.Duration(min(100, max(25, h.RTTMS/2)) * float64(time.Millisecond)))
		quiet := max(150*time.Millisecond, time.Duration(3*h.RTTMS*float64(time.Millisecond)))
		if h.Failures >= 3 && time.Since(path.lastSuccess) >= quiet {
			if h.Healthy {
				h.HealthDowns++
			}
			h.Healthy = false
			path.retryAt = time.Now().Add(300 * time.Millisecond)
		}
		c.wakeAdmissionsLocked()
		return
	}
	if !h.Healthy {
		h.Recoveries++
	}
	h.Healthy = true
	c.pathLocked(addr).lastSuccess = time.Now()
	h.Replies++
	if op == opWrite {
		h.Uploaded += uint64(size)
	}
	if op == opRead {
		h.Downloaded += uint64(size)
	}
	h.Failures = 0
	// Physical lease release wakes the next waiter; do not broadcast twice.
	// A successful idle long-poll includes server wait time, not network RTT.
	if op != opRead || size > 0 {
		rtt := float64(elapsed.Microseconds()) / 1000
		if h.MinRTTMS == 0 || rtt < h.MinRTTMS {
			h.MinRTTMS = rtt
		}
		if h.RTTMS == 0 {
			h.RTTMS = rtt
			h.RTTVarianceMS = rtt / 2
		} else {
			h.RTTVarianceMS = .75*h.RTTVarianceMS + .25*math.Abs(rtt-h.RTTMS)
			h.RTTMS = .875*h.RTTMS + .125*rtt
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
			capacity := c.Profile.Window
			if path := c.paths[addr]; path != nil {
				capacity = path.limit
				pending = max(pending, path.flight) + path.waiting
			}
			candidate := rtt * (1 + float64(pending)/float64(capacity))
			cost = math.Min(cost, candidate)
			if throughput == 0 {
				capacity := float64(chunkSize)
				if len(operation) > 0 && operation[0] == opWrite {
					capacity = float64(c.uploadSize)
				}
				throughput = capacity * 1000 / math.Max(rtt, 1)
			}
			rate += throughput * float64(capacity) / (1 + float64(pending)/float64(capacity))
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
		// Reuse the protected resolver socket. The tracked transport interrupts
		// a canceled probe immediately, unlike a fresh DNS Client read deadline.
		reply, e, _ := c.exchangeDNSTracked(probeCtx, m, addr, "udp", opProbe)
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
	c.testLatency(ctx, emit, false)
}
func (c *Client) testLatency(ctx context.Context, emit func(ResolverHealth), skipActive bool) {
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
					// A tiny health probe must not erase accumulated loss diagnostics
					// or replace the loaded-path RTT estimator.
					if !old.Healthy && h.Healthy {
						old.Failures = 0
						old.Recoveries++
					}
					// Do not let two tiny lost probes erase concurrent payload
					// progress (or a reply newer than this probe run).
					if !h.Healthy && time.Since(c.pathLocked(addr).lastSuccess) < time.Second {
						h.Healthy = old.Healthy
					}
					if old.Healthy && !h.Healthy {
						old.HealthDowns++
					}
					old.Healthy = h.Healthy
					if h.Healthy {
						old.Failures = 0
					}
					if old.RTTMS == 0 {
						old.RTTMS = h.RTTMS
						old.RTTVarianceMS = h.RTTMS / 2
					}
					old.Checked = h.Checked
					h = *old
				}
				c.health[addr] = &h
				c.wakeAdmissionsLocked()
				c.adaptiveMu.Unlock()
				if emit != nil {
					emit(h)
				}
			}
		}()
	}
loop:
	for _, addr := range pool {
		if skipActive {
			c.adaptiveMu.Lock()
			h, p := c.health[addr], c.paths[addr]
			recent := h != nil && h.Healthy && p != nil && time.Since(p.lastSuccess) < 3*time.Second
			c.adaptiveMu.Unlock()
			if recent {
				continue
			}
		}
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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, 12*time.Second)
			c.testLatency(ctx, nil, true)
			cancel()
			if c.Profile.Automatic {
				c.regroup()
			}
		}
	}
}
