package core

import (
	"net/netip"
	"strings"
	"testing"
)

func TestRoutingPrecedence(t *testing.T) {
	p := Profile{DefaultAction: "tunnel", Rules: []Rule{{Domain: "safe.example", Action: "direct"}, {Domain: "example", Action: "block"}, {CIDR: "192.168.0.0/16", Action: "direct"}}}
	for _, tc := range []struct{ host, want string }{{"safe.example", "direct"}, {"a.safe.example", "direct"}, {"badexample", "tunnel"}, {"a.example", "block"}, {"192.168.1.1", "direct"}} {
		if got := p.Route(tc.host, 443, "tcp"); got != tc.want {
			t.Errorf("%s: %s != %s", tc.host, got, tc.want)
		}
	}
}
func TestRangesMergeAndIPv6(t *testing.T) {
	r, e := parseRanges("192.0.2.0/30\n192.0.2.2-192.0.2.8\n2001:db8::/126")
	if e != nil {
		t.Fatal(e)
	}
	if len(r) != 2 {
		t.Fatal(r)
	}
	if r[0].hi != netip.MustParseAddr("192.0.2.8") {
		t.Fatal(r)
	}
	if r[1].hi != netip.MustParseAddr("2001:db8::3") {
		t.Fatal(r)
	}
}
func TestResolverLimit(t *testing.T) {
	p := Profile{Domain: "t.test", Key: strings.Repeat("x", 32)}
	g := Group{}
	for i := 0; i < 101; i++ {
		g.Resolvers = append(g.Resolvers, netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}).String())
	}
	p.Groups = []Group{g}
	if p.Validate() == nil {
		t.Fatal("accepted 101 resolvers")
	}
	p.Groups[0].Resolvers = p.Groups[0].Resolvers[:100]
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestWireAuthenticationAndCapacity(t *testing.T) {
	p := Profile{Domain: strings.Repeat("a", 50) + ".test", Key: strings.Repeat("k", 32)}
	f := frame{Op: opWrite, Session: 123, Stream: 6, Seq: 4, Data: []byte("test")}
	m, e := query(f, p)
	if e != nil {
		t.Fatal(e)
	}
	out, e := extract(m, p.Domain, p.Key)
	if e != nil || string(out.Data) != "test" {
		t.Fatal(e)
	}
	b := f.pack(p.Key)
	b[30] ^= 1
	if _, e = unpack(b, p.Key); e == nil {
		t.Fatal("modified payload accepted")
	}
}
