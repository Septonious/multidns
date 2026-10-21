package core

import (
	"encoding/json"
	"time"
)

// Detailed serialization and optional timing measurements are disabled in
// normal Android use. RTT/loss/capacity feedback remains essential transport work.
func (c *Client) SetDiagnostics(enabled bool) { c.diagnostics.Store(enabled) }
func (c *Client) DiagnosticsEnabled() bool    { return c.diagnostics.Load() }

type resolverSummary struct {
	Transport string  `json:"transport"`
	Endpoint  string  `json:"endpoint"`
	RTT       float64 `json:"rtt_ms"`
	Streams   int     `json:"streams"`
	Status    string  `json:"status"`
	State     string  `json:"state"`
}

func (c *Client) SummaryJSON() string {
	c.adaptiveMu.Lock()
	rows := make([]resolverSummary, 0, len(c.health))
	active := map[string]bool{}
	for _, g := range c.groups {
		for _, addr := range g.Resolvers {
			active[addr] = true
		}
	}
	for _, addr := range c.Profile.ResolverPool() {
		row := resolverSummary{Endpoint: addr, Transport: "udp", State: "yellow", Status: "Checking"}
		h := c.health[addr]
		p := c.paths[addr]
		if h != nil {
			row.RTT = h.RTTMS
			if h.Healthy && h.RTTMS <= 0 && h.Replies == 0 {
				row.Status = "Checking"
			} else if !h.Healthy {
				row.State = "red"
				row.Status = "Unavailable"
			} else if !active[addr] {
				row.Status = "Standby"
			} else {
				row.State = "green"
				row.Status = "Ready"
				if p != nil && (time.Since(p.lastIssue) < 2*time.Second || time.Now().Before(p.readRetryAt)) {
					row.State = "yellow"
					row.Status = "Degraded"
				}
			}
		}
		if p != nil {
			if p.preferTCP {
				row.Transport = "tcp"
			}
			row.Streams = len(p.streams)
			if p.txRTT > 0 && time.Since(p.txChecked) < 10*time.Second {
				row.RTT = p.txRTT
			}
			if row.State == "green" && row.Streams > 0 {
				row.Status = "Active"
			}
		}
		if row.State == "green" && row.RTT > c.writeRTTLimit() {
			row.State = "yellow"
			row.Status = "Degraded"
		}
		rows = append(rows, row)
	}
	groups := len(c.groups)
	c.adaptiveMu.Unlock()
	b, _ := json.Marshal(struct {
		Version    string            `json:"transport_version"`
		Uploaded   uint64            `json:"uploaded"`
		Downloaded uint64            `json:"downloaded"`
		Streams    int64             `json:"streams"`
		GroupCount int               `json:"group_count"`
		Resolvers  []resolverSummary `json:"resolvers"`
		Debug      bool              `json:"debug_enabled"`
	}{"0.3.5", c.uploaded.Load(), c.downloaded.Load(), c.streams.Load(), groups, rows, c.DiagnosticsEnabled()})
	return string(b)
}
