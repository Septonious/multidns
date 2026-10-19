package core

import (
	"github.com/miekg/dns"
	"net/netip"
	"strings"
	"time"
)

type nameEntry struct {
	name    string
	expires time.Time
}

// DNS observations are scoped to the running client. External encrypted DNS hides
// these associations; IP/CIDR rules still apply when no hostname is available.
func (c *Client) observeDNS(packet []byte) {
	var msg dns.Msg
	if msg.Unpack(packet) != nil || !msg.Response || msg.Rcode != dns.RcodeSuccess || len(msg.Question) != 1 {
		return
	}
	name := strings.TrimSuffix(strings.ToLower(msg.Question[0].Name), ".")
	c.namesMu.Lock()
	defer c.namesMu.Unlock()
	now := time.Now()
	if len(c.names) >= 4096 {
		for ip, entry := range c.names {
			if entry.expires.Before(now) {
				delete(c.names, ip)
			}
		}
	}
	for _, rr := range msg.Answer {
		var ip string
		switch a := rr.(type) {
		case *dns.A:
			ip = a.A.String()
		case *dns.AAAA:
			ip = a.AAAA.String()
		default:
			continue
		}
		if _, exists := c.names[ip]; !exists && len(c.names) >= 4096 {
			continue
		}
		ttl := min(rr.Header().Ttl, 600)
		c.names[ip] = nameEntry{name, now.Add(time.Duration(ttl) * time.Second)}
	}
}
func (c *Client) route(host string, port int, proto string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	alias := host
	ip, ipErr := netip.ParseAddr(host)
	if ipErr == nil {
		c.namesMu.RLock()
		entry, ok := c.names[host]
		c.namesMu.RUnlock()
		if ok && time.Now().Before(entry.expires) {
			alias = entry.name
		}
	}
	for _, r := range c.Profile.Rules {
		if r.Port != 0 && r.Port != port || r.Protocol != "" && r.Protocol != proto {
			continue
		}
		if r.Domain != "" {
			d := strings.TrimSuffix(strings.ToLower(r.Domain), ".")
			if alias != d && (r.Exact || !strings.HasSuffix(alias, "."+d)) {
				continue
			}
		}
		if r.CIDR != "" {
			prefix, e := netip.ParsePrefix(r.CIDR)
			if e != nil || ipErr != nil || !prefix.Contains(ip) {
				continue
			}
		}
		return r.Action
	}
	return c.Profile.DefaultAction
}
