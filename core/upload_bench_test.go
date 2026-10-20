package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Upload-only sinks keep download throughput out of the upload measurement.
// A separate application echo and 26 idle sessions share all DNS paths.
func BenchmarkUploadLoadedRTT(b *testing.B) {
	for _, queued := range []bool{false, true} {
		b.Run(fmt.Sprintf("queue-%t", queued), func(b *testing.B) { benchmarkUpload(b, queued, 8) })
	}
}
func BenchmarkLargePoolUploadLoadedRTT(b *testing.B) {
	for _, count := range []int{8, 20} {
		b.Run(fmt.Sprintf("paths-%d", count), func(b *testing.B) { benchmarkUpload(b, false, count) })
	}
}
func BenchmarkPoolCapacity(b *testing.B) {
	for _, count := range []int{8, 20} {
		b.Run(fmt.Sprintf("paths-%d", count), func(b *testing.B) { benchmarkUpload(b, false, count, 0, 512*1024) })
	}
}
func benchmarkUpload(b *testing.B, queued bool, pathCount int, options ...int) {
	lossEvery, payloadBytes := int64(71), 128*1024
	if len(options) > 0 {
		lossEvery = int64(options[0])
		payloadBytes = options[1]
	}
	p, server := testProfile(b)
	p.Adaptive = true
	p.TimeoutMS = 1200
	p.Groups = nil
	for i := 0; i < pathCount; i++ {
		var active, requests atomic.Int64
		delay := time.Duration([]int{35, 40, 45, 50, 55, 60, 75, 90}[i%8]) * time.Millisecond
		addr := testDNS(b, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			f, _ := extract(m, p.Domain, p.Key)
			if f.Op == opWrite || f.Op == opRead {
				if lossEvery > 0 && requests.Add(1)%lossEvery == 0 {
					return
				}
				n := active.Add(1)
				defer active.Add(-1)
				penalty := time.Duration(0)
				if queued {
					penalty = time.Duration(max(0, n-8)) * 8 * time.Millisecond
				}
				time.Sleep(delay + penalty)
			} else {
				time.Sleep(delay)
			}
			server.ServeDNS(w, m)
		}))
		if i%2 == 0 {
			p.Groups = append(p.Groups, Group{})
		}
		g := &p.Groups[len(p.Groups)-1]
		g.Resolvers = append(g.Resolvers, addr)
	}
	c, err := NewClient(p, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	echo := echoTCP(b)
	for range 26 {
		idle, err := c.DialContext(ctx, "tcp", echo)
		if err != nil {
			b.Fatal(err)
		}
		defer idle.Close()
	}
	interactive, err := c.DialContext(ctx, "tcp", echo)
	if err != nil {
		b.Fatal(err)
	}
	defer interactive.Close()
	interactive.SetDeadline(time.Now().Add(90 * time.Second))
	payload := make([]byte, payloadBytes)
	for i := range payload {
		payload[i] = byte(i*17 + i/257)
	}
	digest := sha256.Sum256(payload)
	sink, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer sink.Close()
	go func() {
		for {
			conn, e := sink.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				for {
					data := make([]byte, len(payload))
					if _, e := io.ReadFull(conn, data); e != nil {
						return
					}
					hash := sha256.Sum256(data)
					if _, e := conn.Write(hash[:]); e != nil {
						return
					}
				}
			}()
		}
	}()
	conns := make([]net.Conn, 4)
	for i := range conns {
		conns[i], err = c.DialContext(ctx, "tcp", sink.Addr().String())
		if err != nil {
			b.Fatal(err)
		}
		defer conns[i].Close()
		conns[i].SetDeadline(time.Now().Add(90 * time.Second))
	}
	stop := make(chan struct{})
	echoDone := make(chan error, 1)
	samples := []float64{}
	go func() {
		for {
			select {
			case <-stop:
				echoDone <- nil
				return
			default:
			}
			at := time.Now()
			data := []byte("loaded application echo")
			if _, e := interactive.Write(data); e != nil {
				echoDone <- e
				return
			}
			out := make([]byte, len(data))
			if _, e := io.ReadFull(interactive, out); e != nil {
				echoDone <- e
				return
			}
			if !bytes.Equal(data, out) {
				echoDone <- fmt.Errorf("echo corruption")
				return
			}
			samples = append(samples, float64(time.Since(at).Microseconds())/1000)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	b.SetBytes(int64(len(payload) * len(conns)))
	var peakData atomic.Int64
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				n := int64(len(c.dataSlots))
				for old := peakData.Load(); n > old; old = peakData.Load() {
					if peakData.CompareAndSwap(old, n) {
						break
					}
				}
			}
		}
	}()
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		errs := make(chan error, len(conns))
		for _, conn := range conns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, e := conn.Write(payload); e != nil {
					errs <- e
					return
				}
				out := make([]byte, len(digest))
				_, e := io.ReadFull(conn, out)
				if e == nil && !bytes.Equal(out, digest[:]) {
					e = fmt.Errorf("upload digest mismatch")
				}
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				b.Fatal(e)
			}
		}
	}
	b.StopTimer()
	close(stop)
	<-sampleDone
	b.ReportMetric(float64(peakData.Load()), "peak-data-flight")
	if e := <-echoDone; e != nil {
		b.Fatal(e)
	}
	sort.Float64s(samples)
	if len(samples) > 0 {
		b.ReportMetric(samples[int(float64(len(samples)-1)*.95)], "app-p95-ms")
		b.ReportMetric(samples[int(float64(len(samples)-1)*.99)], "app-p99-ms")
		b.ReportMetric(samples[len(samples)-1], "app-max-ms")
		b.ReportMetric(float64(len(samples)), "app-samples")
	}
	b.ReportMetric(float64(c.Stats().Retries), "retries")
	b.ReportMetric(float64(c.Stats().Queries), "queries")
	b.ReportMetric(float64(c.Stats().Timeouts), "timeouts")
}
