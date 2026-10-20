package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// The previous release's small-flow benchmark omitted many idle sessions,
// successful long-poll wait and recovery from a collapsed path budget together.
func TestHTTPSProgressWith26IdleStreamsAndCollapsedCapacity(t *testing.T) {
	for _, scenario := range []struct {
		groupSize int
		adaptive  bool
	}{{1, true}, {2, true}, {2, false}} {
		groupSize := scenario.groupSize
		t.Run(fmt.Sprintf("group-size-%d-adaptive-%t", groupSize, scenario.adaptive), func(t *testing.T) {
			p, server := testProfile(t)
			p.Adaptive = scenario.adaptive
			p.TimeoutMS = 1000
			p.Groups = nil
			for i := 0; i < 8; i++ {
				delay := time.Duration([]int{35, 40, 45, 50, 60, 70, 90, 120}[i]) * time.Millisecond
				addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
					f, _ := extract(m, p.Domain, p.Key)
					time.Sleep(delay)
					if f.Op == opRead && f.Seq%3 == 0 {
						time.Sleep(60 * time.Millisecond)
					}
					server.ServeDNS(w, m)
				}))
				if i%groupSize == 0 {
					p.Groups = append(p.Groups, Group{})
				}
				g := &p.Groups[len(p.Groups)-1]
				g.Resolvers = append(g.Resolvers, addr)
			}
			c, err := NewClient(p, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			idle := []net.Conn{}
			defer func() {
				for _, conn := range idle {
					conn.Close()
				}
			}()
			echo := echoTCP(t)
			for range 26 {
				conn, err := c.DialContext(ctx, "tcp", echo)
				if err != nil {
					t.Fatal(err)
				}
				idle = append(idle, conn)
			}
			c.adaptiveMu.Lock()
			for _, addr := range p.ResolverPool() {
				path := c.pathLocked(addr)
				path.limit = 2
				path.lastCut = time.Now().Add(-2 * time.Second)
			}
			c.wakeAdmissionsLocked()
			c.adaptiveMu.Unlock()
			data := make([]byte, 16384)
			for i := range data {
				data[i] = byte(i * 17)
			}
			digest := sha256.Sum256(data)
			expected := append(digest[:], data...)
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posted, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(posted, data) {
					http.Error(w, "invalid upload", 400)
					return
				}
				w.Write(expected)
			}))
			defer target.Close()
			defer target.CloseClientConnections()
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			transport := &http.Transport{DialContext: c.DialContext, TLSClientConfig: &tls.Config{RootCAs: roots}, MaxIdleConnsPerHost: 4}
			defer transport.CloseIdleConnections()
			httpClient := &http.Client{Transport: transport, Timeout: 12 * time.Second}
			start := time.Now()
			var wg sync.WaitGroup
			results := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 2 {
						response, err := httpClient.Post(target.URL, "application/octet-stream", bytes.NewReader(data))
						if err != nil {
							results <- err
							return
						}
						out, err := io.ReadAll(response.Body)
						response.Body.Close()
						if err != nil {
							results <- err
							return
						}
						if !bytes.Equal(out, expected) {
							results <- fmt.Errorf("HTTPS response corruption")
							return
						}
					}
					results <- nil
				}()
			}
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatalf("mixed HTTPS failed: %v; %s", err, c.StatsJSON())
				}
			}
			recovered := 0
			for _, h := range c.healthSnapshot() {
				if h.Capacity > 2 {
					recovered++
				}
			}
			if p.Adaptive && recovered == 0 {
				t.Fatalf("all paths remained collapsed: %s", c.StatsJSON())
			}
			t.Logf("8 verified HTTPS upload/downloads with 26 idle streams in %.3fs; %d/8 paths recovered; diagnostics=%s", time.Since(start).Seconds(), recovered, c.StatsJSON())
		})
	}
}
