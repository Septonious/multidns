package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Keep the latency and payload fixed so before/after measurements exercise the
// pipeline rather than local loopback speed. One in eight exchanges is slower,
// exposing the cost of waiting for a complete batch to finish.
func BenchmarkTunnelTransfer(b *testing.B) {
	for _, direction := range []string{"upload", "download"} {
		b.Run(direction, func(b *testing.B) {
			p, server := testProfile(b)
			p.TimeoutMS = 500
			p.Adaptive = true
			p.Groups = nil
			for i := 0; i < 5; i++ {
				group := Group{Name: fmt.Sprint(i)}
				for j := 0; j < 2; j++ {
					group.Resolvers = append(group.Resolvers, testDNS(b, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
						delay := 40 * time.Millisecond
						if f, err := extract(m, p.Domain, p.Key); err == nil && f.Seq%8 == 7 {
							delay += 40 * time.Millisecond
						}
						time.Sleep(delay)
						server.ServeDNS(w, m)
					})))
				}
				p.Groups = append(p.Groups, group)
			}
			client, err := NewClient(p, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer client.Close()
			payload := bytes.Repeat([]byte("multipath"), 8192)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if direction == "upload" {
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						return
					}
					w.Write([]byte("ok"))
				} else {
					w.Write(payload)
				}
			}))
			defer target.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return client.DialContext(ctx, network, address)
			}}
			defer transport.CloseIdleConnections()
			httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var response *http.Response
				if direction == "upload" {
					response, err = httpClient.Post(target.URL, "application/octet-stream", bytes.NewReader(payload))
				} else {
					response, err = httpClient.Get(target.URL)
				}
				if err != nil {
					b.Fatal(err)
				}
				out, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || direction == "download" && !bytes.Equal(out, payload) || direction == "upload" && string(out) != "ok" {
					b.Fatalf("transfer integrity failed: %v", err)
				}
			}
			b.StopTimer()
			stats := client.Stats()
			b.ReportMetric(float64(stats.Queries)/float64(b.N), "queries/transfer")
			b.ReportMetric(float64(stats.Retries)/float64(b.N), "retries/transfer")
		})
	}
}
