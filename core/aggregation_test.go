package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Eight heterogeneous resolvers, four pairs, and fourteen idle destination
// streams model the user's pool more closely than one clean loopback stream.
func BenchmarkAsymmetricPool(b *testing.B) {
	for _, direction := range []string{"upload", "download"} {
		b.Run(direction, func(b *testing.B) {
			p, server := testProfile(b)
			p.Adaptive = true
			p.Window = 8
			p.TimeoutMS = 1000
			p.Groups = nil
			delays := []time.Duration{44, 54, 44, 84, 46, 157, 51, 204}
			var drops atomic.Uint64
			for i := 0; i < 4; i++ {
				group := Group{Name: fmt.Sprint(i)}
				for j := 0; j < 2; j++ {
					delay := delays[i*2+j] * time.Millisecond
					slots := make(chan struct{}, 8)
					addr := testDNS(b, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
						f, err := extract(m, p.Domain, p.Key)
						if err == nil && (f.Op == opRead || f.Op == opWrite) {
							select {
							case slots <- struct{}{}:
								defer func() { <-slots }()
							default:
								drops.Add(1)
								return
							}
						}
						time.Sleep(delay)
						server.ServeDNS(w, m)
					}))
					group.Resolvers = append(group.Resolvers, addr)
				}
				p.Groups = append(p.Groups, group)
			}
			c, err := NewClient(p, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			c.TestLatency(ctx, nil)
			for range 14 {
				idle, err := c.DialContext(ctx, "tcp", echoTCP(b))
				if err != nil {
					b.Fatal(err)
				}
				defer idle.Close()
			}
			c.adaptiveMu.Lock()
			c.pathLocked(p.Groups[2].Resolvers[0]).limit = 2
			c.adaptiveMu.Unlock()
			payload := bytes.Repeat([]byte("pool-throughput-"), 16384)
			digest := fmt.Sprintf("%x", sha256.Sum256(payload))
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if direction == "upload" {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					fmt.Fprintf(w, "%x", sha256.Sum256(data))
				} else {
					w.Write(payload)
				}
			}))
			defer target.Close()
			tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return c.DialContext(ctx, network, address)
			}}
			defer tr.CloseIdleConnections()
			hc := &http.Client{Transport: tr, Timeout: 45 * time.Second}
			probed := make(chan []time.Duration, 1)
			probeCtx, stopProbes := context.WithCancel(ctx)
			go func() {
				var timings []time.Duration
				for probeCtx.Err() == nil {
					start := time.Now()
					pctx, stop := context.WithTimeout(probeCtx, time.Second)
					err := c.Probe(pctx)
					stop()
					if err == nil {
						timings = append(timings, time.Since(start))
					}
					select {
					case <-time.After(100 * time.Millisecond):
					case <-probeCtx.Done():
					}
				}
				probed <- timings
			}()
			before := c.Stats()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				var reply *http.Response
				if direction == "upload" {
					reply, err = hc.Post(target.URL, "application/octet-stream", bytes.NewReader(payload))
				} else {
					reply, err = hc.Get(target.URL)
				}
				if err != nil {
					b.Fatal(err)
				}
				data, err := io.ReadAll(reply.Body)
				reply.Body.Close()
				if err != nil || direction == "upload" && string(data) != digest || direction == "download" && !bytes.Equal(data, payload) {
					b.Fatalf("payload mismatch: %v", err)
				}
			}
			b.StopTimer()
			stopProbes()
			timings := <-probed
			if len(timings) > 0 {
				sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
				b.ReportMetric(float64(timings[(len(timings)-1)*95/100].Microseconds())/1000, "probe-p95-ms")
			}
			b.Log("pool diagnostics:", c.StatsJSON())
			after := c.Stats()
			b.ReportMetric(float64(after.Queries-before.Queries)/float64(b.N), "queries/transfer")
			b.ReportMetric(float64(after.Retries-before.Retries)/float64(b.N), "retries/transfer")
			b.ReportMetric(float64(drops.Load())/float64(b.N), "dropped/transfer")
		})
	}
}
