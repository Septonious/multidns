package core

import (
	"bytes"
	"context"
	"fmt"
	"github.com/miekg/dns"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Eight bulk flows and 26 idle connections share application-level echo traffic.
// Identical source can run against the previous release for a loaded RTT comparison.
func BenchmarkManyStreamApplication(b *testing.B) {
	for _, groupSize := range []int{1, 2} {
		b.Run(fmt.Sprintf("group-size-%d", groupSize), func(b *testing.B) { benchmarkManyStreams(b, groupSize) })
	}
}
func benchmarkManyStreams(b *testing.B, groupSize int) {
	p, server := testProfile(b)
	p.Adaptive = true
	p.TimeoutMS = 1200
	p.Groups = nil
	var peak atomic.Int64
	for i := 0; i < 8; i++ {
		delay := time.Duration([]int{35, 40, 45, 50, 60, 70, 90, 120}[i]) * time.Millisecond
		var active atomic.Int64
		addr := testDNS(b, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			f, _ := extract(m, p.Domain, p.Key)
			if f.Op == opRead || f.Op == opWrite {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(delay + time.Duration(max(0, n-8))*8*time.Millisecond)
			}
			server.ServeDNS(w, m)
		}))
		if i%groupSize == 0 {
			p.Groups = append(p.Groups, Group{Name: fmt.Sprint(i)})
		}
		g := &p.Groups[len(p.Groups)-1]
		g.Resolvers = append(g.Resolvers, addr)
	}
	c, err := NewClient(p, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	addr := echoTCP(b)
	interactive, err := c.DialContext(ctx, "tcp", addr)
	if err != nil {
		b.Fatal(err)
	}
	defer interactive.Close()
	interactive.SetDeadline(time.Now().Add(85 * time.Second))
	for range 26 {
		idle, err := c.DialContext(ctx, "tcp", addr)
		if err != nil {
			b.Fatal(err)
		}
		defer idle.Close()
	}
	conns := make([]io.ReadWriteCloser, 8)
	for i := range conns {
		conns[i], err = c.DialContext(ctx, "tcp", addr)
		if err != nil {
			b.Fatal(err)
		}
		defer conns[i].Close()
	}
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
			payload := []byte("interactive-echo")
			if _, err := interactive.Write(payload); err != nil {
				echoDone <- err
				return
			}
			out := make([]byte, len(payload))
			if _, err := io.ReadFull(interactive, out); err != nil {
				echoDone <- err
				return
			}
			if !bytes.Equal(out, payload) {
				echoDone <- fmt.Errorf("echo corruption")
				return
			}
			samples = append(samples, float64(time.Since(at).Microseconds())/1000)
			time.Sleep(30 * time.Millisecond)
		}
	}()
	payload := make([]byte, 65536)
	for i := range payload {
		payload[i] = byte(i * 13)
	}
	b.SetBytes(int64(len(payload) * len(conns)))
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for _, conn := range conns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				done := make(chan error, 1)
				go func() { _, err := conn.Write(payload); done <- err }()
				out := make([]byte, len(payload))
				_, err := io.ReadFull(conn, out)
				if err == nil && !bytes.Equal(out, payload) {
					err = fmt.Errorf("bulk corruption")
				}
				if err != nil {
					errs <- err
				}
				if err := <-done; err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	close(stop)
	if err := <-echoDone; err != nil {
		b.Fatal(err)
	}
	sort.Float64s(samples)
	if len(samples) > 0 {
		b.ReportMetric(samples[int(float64(len(samples)-1)*.95)], "app-p95-ms")
		b.ReportMetric(float64(len(samples)), "app-samples")
	}
	b.ReportMetric(float64(peak.Load()), "peak-path-requests")
	b.ReportMetric(float64(c.Stats().Retries), "retries")
	var queuePeak float64
	for _, h := range c.healthSnapshot() {
		queuePeak = max(queuePeak, h.MaxQueueMS)
	}
	b.ReportMetric(queuePeak, "max-admission-ms")
}
