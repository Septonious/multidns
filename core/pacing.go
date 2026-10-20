package core

import (
	"context"
	"time"
)

// Smooth bulk writes over one ACK clock instead of firing a complete window at
// once. Short writes and controls bypass the pacer. Leases remain held while
// pacing, so scheduled packets cannot run ahead of the shared credit budget.
func (c *Client) paceUpload(ctx context.Context, addr string) error {
	if !c.Profile.Adaptive {
		return nil
	}
	c.adaptiveMu.Lock()
	p := c.pathLocked(addr)
	rtt := p.txRTT
	if rtt == 0 {
		if h := c.health[addr]; h != nil {
			rtt = h.RTTMS
		}
	}
	if rtt <= 0 {
		c.adaptiveMu.Unlock()
		return nil
	}
	interval := max(100*time.Microsecond, time.Duration(rtt*float64(time.Millisecond)/float64(max(2, p.limit))))
	now := time.Now()
	at := p.nextSend
	if at.Before(now) {
		at = now
	}
	// Canceled sends leave a small gap, never a growing queue of future slots.
	at = minTime(at, now.Add(time.Duration(rtt*float64(time.Millisecond))))
	p.nextSend = at.Add(interval)
	delay := at.Sub(now)
	c.adaptiveMu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.diagnostics.Load() {
		return nil
	}
	c.adaptiveMu.Lock()
	if h := c.health[addr]; h != nil {
		h.PacedQueries++
		h.PacingWaitMS = .875*h.PacingWaitMS + .125*float64(time.Since(now).Microseconds())/1000
	}
	c.adaptiveMu.Unlock()
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
