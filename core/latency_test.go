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

// Application echo shares the DNS data path with four simultaneous transfers.
// It cannot bypass admission as a control-only Probe would.
func BenchmarkLoadedApplication(b *testing.B) {
	p, server := testProfile(b)
	p.Adaptive = true
	p.TimeoutMS = 700
	p.Groups = nil
	var peak atomic.Int64
	for i := 0; i < 4; i++ {
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
				time.Sleep(20*time.Millisecond + time.Duration(max(0, n-8))*8*time.Millisecond)
			}
			server.ServeDNS(w, m)
		}))
		p.Groups = append(p.Groups, Group{Resolvers: []string{addr}, Name: fmt.Sprint(i)})
	}
	c, err := NewClient(p, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	addr := echoTCP(b)
	interactive, err := c.DialContext(ctx, "tcp", addr)
	if err != nil {
		b.Fatal(err)
	}
	defer interactive.Close()
	interactive.SetDeadline(time.Now().Add(35 * time.Second))
	conns := make([]io.ReadWriteCloser, 4)
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
	payload := make([]byte, 32768)
	for i := range payload {
		payload[i] = byte(i * 13)
	}
	b.SetBytes(int64(len(payload) * len(conns)))
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
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
}
