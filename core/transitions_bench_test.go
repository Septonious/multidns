package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/miekg/dns"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the phone's actual SOCKS path, then a direction change and quiet
// recovery. Each resolver shares physical service capacity across directions,
// but large read replies additionally see downstream queueing and sparse loss.
func BenchmarkPoolTransitions(b *testing.B) {
	for _, count := range []int{8, 20} {
		for _, size := range []int{1, 2} {
			b.Run(fmt.Sprintf("paths-%d/group-%d", count, size), func(b *testing.B) { benchmarkPoolTransitions(b, count, size) })
		}
	}
}
func benchmarkPoolTransitions(b *testing.B, count, size int) {
	p, server := testProfile(b)
	p.Adaptive = true
	p.TimeoutMS = 1000
	p.Groups = nil
	for i := 0; i < count; i++ {
		delay := time.Duration([]int{35, 40, 45, 50, 55, 75, 90, 95}[i%8]) * time.Millisecond
		var reads, all, received atomic.Int64
		addr := testDNS(b, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			f, _ := extract(m, p.Domain, p.Key)
			n := all.Add(1)
			defer all.Add(-1)
			extra := time.Duration(max(0, n-40)) * 3 * time.Millisecond
			if f.Op == opRead {
				if received.Add(1)%67 == 0 {
					return
				}
				r := reads.Add(1)
				defer reads.Add(-1)
				extra += time.Duration(max(0, r-8)) * 9 * time.Millisecond
			}
			time.Sleep(delay + extra)
			server.ServeDNS(w, m)
		}))
		if i%size == 0 {
			p.Groups = append(p.Groups, Group{})
		}
		p.Groups[len(p.Groups)-1].Resolvers = append(p.Groups[len(p.Groups)-1].Resolvers, addr)
	}
	c, err := NewClient(p, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	go c.ServeSOCKS(listener)
	dialer, err := proxy.SOCKS5("tcp", listener.Addr().String(), nil, proxy.Direct)
	if err != nil {
		b.Fatal(err)
	}
	dial := dialer.(proxy.ContextDialer)
	echo := echoTCP(b)
	for range 8 {
		idle, e := dial.DialContext(ctx, "tcp", echo)
		if e != nil {
			b.Fatal(e)
		}
		defer idle.Close()
	}
	interactive, e := dial.DialContext(ctx, "tcp", echo)
	if e != nil {
		b.Fatal(e)
	}
	defer interactive.Close()
	interactive.SetDeadline(time.Now().Add(95 * time.Second))
	payload := make([]byte, 512*1024)
	for i := range payload {
		payload[i] = byte(i*19 + i/257)
	}
	upload := payload[:256*1024]
	digest := fmt.Sprintf("%x", sha256.Sum256(upload))
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			data, e := io.ReadAll(r.Body)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			fmt.Fprintf(w, "%x", sha256.Sum256(data))
		} else {
			w.Write(payload)
		}
	}))
	defer target.Close()
	tr := &http.Transport{DialContext: dial.DialContext}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 80 * time.Second}
	var mu sync.Mutex
	samples := []float64{}
	stop := make(chan struct{})
	echoDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				echoDone <- nil
				return
			default:
			}
			at := time.Now()
			msg := []byte("transition application echo")
			_, e := interactive.Write(msg)
			if e != nil {
				echoDone <- e
				return
			}
			out := make([]byte, len(msg))
			_, e = io.ReadFull(interactive, out)
			if e != nil {
				echoDone <- e
				return
			}
			if !bytes.Equal(out, msg) {
				echoDone <- fmt.Errorf("echo corruption")
				return
			}
			mu.Lock()
			samples = append(samples, float64(time.Since(at).Microseconds())/1000)
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
		}
	}()
	var cleanup sync.Once
	finish := func() {
		cleanup.Do(func() {
			close(stop)
			if e := <-echoDone; e != nil {
				b.Error(e)
			}
		})
	}
	defer finish()
	phase := func(up bool) time.Duration {
		at := time.Now()
		errs := make(chan error, 3)
		for range 3 {
			go func() {
				var r *http.Response
				var e error
				if up {
					r, e = hc.Post(target.URL, "application/octet-stream", bytes.NewReader(upload))
				} else {
					r, e = hc.Get(target.URL)
				}
				if e == nil {
					out, readErr := io.ReadAll(r.Body)
					r.Body.Close()
					e = readErr
					if e == nil && (up && string(out) != digest || !up && !bytes.Equal(out, payload)) {
						e = fmt.Errorf("payload mismatch")
					}
				}
				errs <- e
			}()
		}
		for range 3 {
			if e := <-errs; e != nil {
				b.Fatal(e)
			}
		}
		return time.Since(at)
	}
	var down, up time.Duration
	b.ResetTimer()
	for range b.N {
		down += phase(false)
		up += phase(true)
	}
	b.StopTimer()
	time.Sleep(600 * time.Millisecond)
	finish()
	b.ReportMetric(float64(3*len(payload)*b.N)*8/down.Seconds()/1e6, "download-Mbps")
	b.ReportMetric(float64(3*len(upload)*b.N)*8/up.Seconds()/1e6, "upload-Mbps")
	mu.Lock()
	sort.Float64s(samples)
	if len(samples) > 0 {
		b.ReportMetric(samples[(len(samples)-1)*95/100], "app-p95-ms")
		b.ReportMetric(samples[len(samples)-1], "app-max-ms")
		b.ReportMetric(float64(len(samples)), "app-samples")
	}
	mu.Unlock()
	b.ReportMetric(float64(c.Stats().Retries), "retries")
	b.ReportMetric(float64(c.Stats().Timeouts), "timeouts")
	downs := uint64(0)
	for _, h := range c.healthSnapshot() {
		downs += h.HealthDowns
	}
	b.ReportMetric(float64(downs), "health-downs")
}
