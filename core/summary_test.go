package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLightSummaryCountsUniqueResolverStreamsWithoutDebugSerialization(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDiagnostics(false)
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.adaptiveMu.Lock()
	one := c.leaseFlowLocked(addr, opWrite, true, 1)
	read := c.leaseFlowLocked(addr, opRead, false, 1)
	two := c.leaseFlowLocked(addr, opWrite, true, 2)
	c.adaptiveMu.Unlock()
	check := func(want int) {
		t.Helper()
		var data struct {
			Resolvers []struct {
				Endpoint string `json:"endpoint"`
				Streams  int    `json:"streams"`
			}
			Debug bool `json:"debug_enabled"`
		}
		if err := json.Unmarshal([]byte(c.SummaryJSON()), &data); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range data.Resolvers {
			if row.Endpoint == addr {
				found = true
				if row.Streams != want {
					t.Fatalf("stream count %d want %d", row.Streams, want)
				}
			}
		}
		if !found {
			t.Fatal("resolver missing")
		}
		if data.Debug {
			t.Fatal("debug enabled by default")
		}
		var raw map[string]any
		json.Unmarshal([]byte(c.SummaryJSON()), &raw)
		if _, ok := raw["timeouts"]; ok {
			t.Fatal("light summary exported detailed counters")
		}
	}
	check(2)
	one()
	check(2)
	read()
	check(1)
	two()
	check(0)
	c.SetDiagnostics(true)
	if !c.DiagnosticsEnabled() {
		t.Fatal("diagnostic switch failed")
	}
}

func TestResolverCardStateDistinguishesUnknownLossAndConfirmedOutage(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	state := func() string {
		var data struct {
			Resolvers []resolverSummary `json:"resolvers"`
		}
		json.Unmarshal([]byte(c.SummaryJSON()), &data)
		for _, r := range data.Resolvers {
			if r.Endpoint == addr {
				return r.State
			}
		}
		return ""
	}
	if state() != "yellow" {
		t.Fatal("untested path was shown as reachable")
	}
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	if state() != "green" {
		t.Fatal("authenticated path is not green")
	}
	c.recordStart(addr)
	c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	if state() != "yellow" {
		t.Fatal("isolated loss should be degraded, not offline")
	}
	c.adaptiveMu.Lock()
	c.pathLocked(addr).lastSuccess = time.Now().Add(-time.Second)
	c.adaptiveMu.Unlock()
	for range 3 {
		c.recordStart(addr)
		c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	}
	if state() != "red" {
		t.Fatal("confirmed outage is not red")
	}
}
