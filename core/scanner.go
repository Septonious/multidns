package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type ScanOptions struct {
	ResumeAt  uint64  `json:"resume_at"`
	Ranges    string  `json:"ranges"`
	Workers   int     `json:"workers"`
	TimeoutMS int     `json:"timeout_ms"`
	Profile   Profile `json:"profile"`
	NSZone    string  `json:"ns_zone"`
}
type ScanResult struct {
	Transport  string `json:"transport,omitempty"`
	Index      uint64 `json:"index"`
	Endpoint   string `json:"endpoint"`
	Responding bool   `json:"responding"`
	TXT        bool   `json:"txt"`
	NS         bool   `json:"ns_a"`
	Tunnel     bool   `json:"tunnel"`
	RTTMS      int64  `json:"rtt_ms"`
	Error      string `json:"error,omitempty"`
}
type interval struct {
	lo, hi netip.Addr
	port   string
}

func parseRanges(input string) ([]interval, error) {
	out := []interval{}
	for _, s := range strings.FieldsFunc(input, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' || r == '\r' || r == '\t' }) {
		var lo, hi netip.Addr
		port := "53"
		if p, e := netip.ParsePrefix(s); e == nil {
			p = p.Masked()
			lo = p.Addr()
			b := lo.AsSlice()
			bits := p.Bits()
			for i := range b {
				remaining := bits - i*8
				if remaining <= 0 {
					b[i] = 255
				} else if remaining < 8 {
					b[i] |= byte((1 << (8 - remaining)) - 1)
				}
			}
			hi, _ = netip.AddrFromSlice(b)
		} else if pair := strings.Split(s, "-"); len(pair) == 2 {
			var e error
			lo, e = netip.ParseAddr(pair[0])
			if e != nil {
				return nil, e
			}
			hi, e = netip.ParseAddr(pair[1])
			if e != nil {
				return nil, e
			}
		} else {
			a, e := Endpoint(s)
			if e != nil {
				return nil, e
			}
			host, p, _ := net.SplitHostPort(a)
			lo, _ = netip.ParseAddr(host)
			hi = lo
			port = p
		}
		if lo.Is4In6() {
			lo = lo.Unmap()
		}
		if hi.Is4In6() {
			hi = hi.Unmap()
		}
		if lo.BitLen() != hi.BitLen() || lo.Compare(hi) > 0 {
			return nil, fmt.Errorf("invalid range %s", s)
		}
		out = append(out, interval{lo, hi, port})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("enter at least one IP or range")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].port != out[j].port {
			return out[i].port < out[j].port
		}
		return out[i].lo.Compare(out[j].lo) < 0
	})
	merged := []interval{}
	for _, r := range out {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			if last.port == r.port && last.lo.BitLen() == r.lo.BitLen() && (r.lo.Compare(last.hi) <= 0 || r.lo == last.hi.Next()) {
				if r.hi.Compare(last.hi) > 0 {
					last.hi = r.hi
				}
				continue
			}
		}
		merged = append(merged, r)
	}
	return merged, nil
}
func Scan(ctx context.Context, o ScanOptions, protector Protector, emit func(ScanResult)) error {
	ranges, e := parseRanges(o.Ranges)
	if e != nil {
		return e
	}
	if o.Workers < 1 || o.Workers > 256 {
		return fmt.Errorf("workers must be 1..256")
	}
	if o.TimeoutMS < 100 || o.TimeoutMS > 30000 {
		return fmt.Errorf("timeout must be 100..30000 ms")
	}
	if e = o.Profile.Validate(); e != nil {
		return e
	}
	type job struct {
		endpoint string
		index    uint64
	}
	jobs := make(chan job, o.Workers)
	var wg sync.WaitGroup
	for i := 0; i < o.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				if ctx.Err() != nil {
					return
				}
				r := scanOne(ctx, o, target.endpoint, protector)
				r.Index = target.index
				if ctx.Err() == nil {
					emit(r)
				}
			}
		}()
	}
	var index uint64
loop:
	for _, r := range ranges {
		for addr := r.lo; ; addr = addr.Next() {
			if index < o.ResumeAt {
				index++
				if addr == r.hi {
					break
				}
				continue
			}
			select {
			case jobs <- job{net.JoinHostPort(addr.String(), r.port), index}:
			case <-ctx.Done():
				break loop
			}
			index++
			if addr == r.hi {
				break
			}
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}
func scanOne(ctx context.Context, o ScanOptions, endpoint string, protector Protector) ScanResult {
	result := ScanResult{Endpoint: endpoint}
	p := o.Profile
	p.Automatic = false
	p.Adaptive = false
	p.Pool = nil
	p.Groups = []Group{{Resolvers: []string{endpoint}}}
	p.TimeoutMS = o.TimeoutMS
	c, e := NewClient(p, protector)
	if e != nil {
		result.Error = e.Error()
		return result
	}
	defer c.Close()
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(2*o.TimeoutMS)*time.Millisecond)
	defer cancel()
	e = c.Probe(probeCtx)
	result.RTTMS = time.Since(start).Milliseconds()
	result.Tunnel = e == nil
	result.TXT = e == nil
	network := "udp"
	if e == nil {
		for _, h := range c.healthSnapshot() {
			if h.Endpoint == endpoint {
				network = h.Transport
				result.Transport = network
				if h.RTTMS > 0 {
					result.RTTMS = max(1, int64(h.RTTMS))
				}
			}
		}
	}
	dc := &dns.Client{Net: network, Timeout: time.Duration(o.TimeoutMS) * time.Millisecond, Dialer: c.dialer}
	zone := strings.TrimSuffix(o.NSZone, ".")
	if zone == "" {
		parts := strings.Split(p.Domain, ".")
		if len(parts) > 2 {
			zone = strings.Join(parts[1:], ".")
		} else {
			zone = p.Domain
		}
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(zone), dns.TypeNS)
	nsCtx, nsCancel := context.WithTimeout(ctx, time.Duration(o.TimeoutMS)*time.Millisecond)
	defer nsCancel()
	r, _, err := dc.ExchangeContext(nsCtx, m, endpoint)
	if err == nil {
		result.Responding = true
		if r.Rcode == dns.RcodeSuccess {
			records := append(append([]dns.RR{}, r.Answer...), r.Ns...)
			for _, rr := range records {
				ns, ok := rr.(*dns.NS)
				if !ok {
					continue
				}
				q := new(dns.Msg)
				q.SetQuestion(ns.Ns, dns.TypeA)
				a, _, err := dc.ExchangeContext(nsCtx, q, endpoint)
				if err == nil && a.Rcode == dns.RcodeSuccess {
					for _, rr := range a.Answer {
						if _, ok := rr.(*dns.A); ok {
							result.NS = true
						}
					}
				}
				if result.NS {
					break
				}
			}
		}
	}
	result.Responding = result.Responding || result.Tunnel
	if e != nil {
		result.Error = e.Error()
	}
	return result
}
func (r ScanResult) JSON() string { b, _ := json.Marshal(r); return string(b) }
