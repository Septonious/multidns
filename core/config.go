package core

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type Group struct {
	Name      string   `json:"name"`
	Resolvers []string `json:"resolvers"`
}
type Rule struct {
	Domain   string `json:"domain,omitempty"`
	Exact    bool   `json:"exact,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	Port     int    `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Action   string `json:"action"`
}
type Profile struct {
	Pool       []string `json:"resolver_pool,omitempty"`
	Automatic  bool     `json:"automatic_groups"`
	Adaptive   bool     `json:"adaptive"`
	GroupSize  int      `json:"group_size"`
	GroupCount int      `json:"group_count"`

	Name          string   `json:"name"`
	Domain        string   `json:"domain"`
	ClientID      string   `json:"client_id"`
	Key           string   `json:"key"`
	Groups        []Group  `json:"groups"`
	Mode          string   `json:"mode"`
	Rules         []Rule   `json:"rules"`
	DefaultAction string   `json:"default_action"`
	TimeoutMS     int      `json:"timeout_ms"`
	Window        int      `json:"window"`
	AppMode       string   `json:"app_mode"`
	Apps          []string `json:"apps"`
}

func ParseProfile(s string) (Profile, error) {
	var p Profile
	e := json.Unmarshal([]byte(s), &p)
	if e == nil {
		e = p.Validate()
	}
	return p, e
}
func (p *Profile) Validate() error {
	p.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.Domain)), ".")
	if p.Domain == "" || len(p.Domain) > 100 {
		return fmt.Errorf("tunnel domain must be 1..100 characters")
	}
	for _, l := range strings.Split(p.Domain, ".") {
		if len(l) == 0 || len(l) > 63 {
			return fmt.Errorf("invalid domain label")
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("invalid domain")
			}
		}
	}
	if len(p.Key) < 16 {
		return fmt.Errorf("authentication key must contain at least 16 characters")
	}
	if p.ClientID == "" {
		p.ClientID = "android"
	}
	if p.Mode == "" {
		p.Mode = "stripe"
	}
	if p.Mode != "stripe" && p.Mode != "affinity" {
		return fmt.Errorf("mode must be stripe or affinity")
	}
	if p.DefaultAction == "" {
		p.DefaultAction = "tunnel"
	}
	if !validAction(p.DefaultAction) {
		return fmt.Errorf("invalid default action")
	}
	if p.TimeoutMS == 0 {
		p.TimeoutMS = 1500
	}
	if p.TimeoutMS < 100 || p.TimeoutMS > 30000 {
		return fmt.Errorf("timeout must be 100..30000 ms")
	}
	if p.Window == 0 {
		p.Window = 8
	}
	if p.Window < 1 || p.Window > 32 {
		return fmt.Errorf("window must be 1..32")
	}
	if len(p.Groups) == 0 && len(p.Pool) > 0 {
		p.Groups = []Group{{Name: "Candidates", Resolvers: append([]string(nil), p.Pool...)}}
	}
	if p.GroupSize == 0 {
		p.GroupSize = 2
	}
	if p.GroupCount == 0 {
		p.GroupCount = 5
	}
	if p.GroupSize < 1 || p.GroupSize > 100 || p.GroupCount < 1 || p.GroupCount > 100 || p.GroupSize*p.GroupCount > 100 {
		return fmt.Errorf("automatic groups require size/count 1..100 and capacity at most 100")
	}
	if len(p.Groups) == 0 {
		return fmt.Errorf("at least one resolver group is required")
	}
	n := 0
	seen := map[string]bool{}
	for i := range p.Groups {
		g := &p.Groups[i]
		if len(g.Resolvers) == 0 {
			return fmt.Errorf("group %d is empty", i+1)
		}
		for j, s := range g.Resolvers {
			a, e := Endpoint(s)
			if e != nil {
				return e
			}
			if seen[a] {
				return fmt.Errorf("duplicate resolver %s", a)
			}
			seen[a] = true
			g.Resolvers[j] = a
			n++
		}
	}
	poolSeen := map[string]bool{}
	for _, g := range p.Groups {
		for _, addr := range g.Resolvers {
			poolSeen[addr] = true
		}
	}
	for i, addr := range p.Pool {
		normalized, e := Endpoint(addr)
		if e != nil {
			return e
		}
		p.Pool[i] = normalized
		poolSeen[normalized] = true
	}
	if len(poolSeen) > 100 {
		return fmt.Errorf("maximum 100 candidate resolvers")
	}
	if n > 100 {
		return fmt.Errorf("maximum 100 active resolvers")
	}
	if p.AppMode != "" && p.AppMode != "all" && p.AppMode != "include" && p.AppMode != "exclude" {
		return fmt.Errorf("invalid app mode")
	}
	if p.AppMode == "include" && len(p.Apps) == 0 {
		return fmt.Errorf("select at least one included app")
	}
	for _, r := range p.Rules {
		if !validAction(r.Action) {
			return fmt.Errorf("invalid rule action")
		}
		if r.CIDR != "" {
			if _, e := netip.ParsePrefix(r.CIDR); e != nil {
				return e
			}
		}
		if r.Port < 0 || r.Port > 65535 {
			return fmt.Errorf("invalid rule port")
		}
		if r.Protocol != "" && r.Protocol != "tcp" && r.Protocol != "udp" {
			return fmt.Errorf("invalid rule protocol")
		}
	}
	return nil
}
func validAction(a string) bool { return a == "direct" || a == "tunnel" || a == "block" }
func Endpoint(s string) (string, error) {
	s = strings.TrimSpace(s)
	if ip, e := netip.ParseAddr(s); e == nil {
		return net.JoinHostPort(ip.String(), "53"), nil
	}
	h, p, e := net.SplitHostPort(s)
	if e != nil {
		return "", fmt.Errorf("invalid resolver %q; use IP or IP:port", s)
	}
	if _, e = netip.ParseAddr(h); e != nil {
		return "", fmt.Errorf("resolver must be an IP address")
	}
	port, e := strconv.Atoi(p)
	if e != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid resolver port")
	}
	return net.JoinHostPort(h, strconv.Itoa(port)), nil
}
func (p Profile) Route(host string, port int, proto string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	ip, ipErr := netip.ParseAddr(host)
	for _, r := range p.Rules {
		if r.Port != 0 && r.Port != port || r.Protocol != "" && r.Protocol != proto {
			continue
		}
		if r.Domain != "" {
			d := strings.TrimSuffix(strings.ToLower(r.Domain), ".")
			if host != d && (r.Exact || !strings.HasSuffix(host, "."+d)) {
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
	return p.DefaultAction
}

func (p Profile) ResolverPool() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, g := range p.Groups {
		for _, a := range g.Resolvers {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	for _, a := range p.Pool {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}
