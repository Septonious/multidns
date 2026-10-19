package core

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestAutomaticGroupEdgeCases(t *testing.T) {
	for _, tc := range []struct{ resolvers, size, count, groups, spares int }{{11, 2, 5, 5, 1}, {1, 2, 5, 1, 0}, {3, 2, 5, 2, 0}, {0, 2, 5, 0, 0}, {100, 2, 5, 5, 90}} {
		pool := []ResolverHealth{}
		for i := 0; i < tc.resolvers; i++ {
			pool = append(pool, ResolverHealth{Endpoint: fmt.Sprint(i), Healthy: true, RTTMS: float64(i + 1)})
		}
		groups, spares := FormGroups(pool, tc.size, tc.count)
		if len(groups) != tc.groups || len(spares) != tc.spares {
			t.Fatalf("%+v: got %d groups %d spares", tc, len(groups), len(spares))
		}
		for _, g := range groups {
			if len(g.Resolvers) > tc.size || len(g.Resolvers) == 0 {
				t.Fatal("invalid group size")
			}
		}
	}
}
func TestAdaptivePoliciesAndFailureExclusion(t *testing.T) {
	p, _ := testProfile(t)
	p.Groups = []Group{{Resolvers: []string{"127.0.0.1:10001"}}, {Resolvers: []string{"127.0.0.1:10002"}}}
	c, _ := NewClient(p, nil)
	defer c.Close()
	c.Profile.Adaptive = true
	c.adaptiveMu.Lock()
	c.health = map[string]*ResolverHealth{"127.0.0.1:10001": {Endpoint: "127.0.0.1:10001", Healthy: true, RTTMS: 10, Goodput: 1000}, "127.0.0.1:10002": {Endpoint: "127.0.0.1:10002", Healthy: true, RTTMS: 100, Goodput: 100000}}
	c.adaptiveMu.Unlock()
	if c.chooseGroup(1, false) != 0 {
		t.Fatal("interactive request ignored low RTT")
	}
	bulk := [2]int{}
	for range 100 {
		bulk[c.chooseGroup(0, true)]++
	}
	if bulk[1] < 90 {
		t.Fatalf("bulk policy ignored capacity: %v", bulk)
	}
	c.adaptiveMu.Lock()
	c.health["127.0.0.1:10002"].Healthy = false
	c.adaptiveMu.Unlock()
	for range 10 {
		if c.chooseGroup(0, true) != 0 {
			t.Fatal("failed path still scheduled")
		}
	}
}
func TestManualLatencyConcurrentAndAuthenticated(t *testing.T) {
	p, s := testProfile(t)
	slow := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) { time.Sleep(50 * time.Millisecond); s.ServeDNS(w, m) }))
	p.Groups = append(p.Groups, Group{Resolvers: []string{slow}})
	c, _ := NewClient(p, nil)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan ResolverHealth, 3)
	c.TestLatency(ctx, func(h ResolverHealth) { results <- h })
	close(results)
	n := 0
	for h := range results {
		n++
		if !h.Healthy {
			t.Fatal("healthy endpoint rejected")
		}
		if h.Endpoint == slow && h.RTTMS < 40 {
			t.Fatal("delay not measured")
		}
	}
	if n != 3 {
		t.Fatal(n)
	}
}
func TestObservedDNSRouting(t *testing.T) {
	p, _ := testProfile(t)
	p.Rules = []Rule{{Domain: "example.org", Action: "direct"}}
	c, _ := NewClient(p, nil)
	defer c.Close()
	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeA)
	m.Response = true
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.org.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("203.0.113.8")}}
	b, _ := m.Pack()
	c.observeDNS(b)
	if c.route("203.0.113.8", 443, "tcp") != "direct" {
		t.Fatal("observed DNS name was ignored")
	}
	if c.route("203.0.113.9", 443, "tcp") != "tunnel" {
		t.Fatal("unrelated IP matched")
	}
}

func TestAdaptiveBulkRatesAreDirectional(t *testing.T) {
	p, _ := testProfile(t)
	p.Groups = []Group{{Resolvers: []string{"127.0.0.1:10001"}}, {Resolvers: []string{"127.0.0.1:10002"}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Profile.Adaptive = true
	c.health = map[string]*ResolverHealth{
		"127.0.0.1:10001": {Healthy: true, RTTMS: 40, UploadRate: 10000, DownloadRate: 1000},
		"127.0.0.1:10002": {Healthy: true, RTTMS: 40, UploadRate: 1000, DownloadRate: 10000},
	}
	for _, operation := range []byte{opWrite, opRead} {
		c.deficits = nil
		counts := [2]int{}
		for range 100 {
			counts[c.chooseGroup(0, true, operation)]++
		}
		want := 0
		if operation == opRead {
			want = 1
		}
		if counts[want] < 85 {
			t.Fatalf("operation %d ignored directional capacity: %v", operation, counts)
		}
	}
}
