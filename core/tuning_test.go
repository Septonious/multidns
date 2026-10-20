package core

import (
	"bytes"
	"context"
	"github.com/miekg/dns"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestXORHasZeroWireOverheadAndAuthenticatesCiphertext(t *testing.T) {
	key := "0123456789abcdef"
	f := frame{Op: opWrite, Session: 42, Stream: 7, Seq: 9, Nonce: freshNonce(), Data: []byte("TLS ciphertext stays exactly the same size")}
	plain := f.pack(key)
	f.XOR = true
	masked := f.pack(key)
	if len(masked) != len(plain) || bytes.Equal(masked[30:len(masked)-8], f.Data) {
		t.Fatal("obfuscation length or mask")
	}
	got, err := unpack(masked, key)
	if err != nil || !got.XOR || got.Op != f.Op || !bytes.Equal(got.Data, f.Data) {
		t.Fatalf("roundtrip: %v", err)
	}
	masked[31] ^= 1
	if _, err = unpack(masked, key); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	masked = f.pack(key)
	masked[1] ^= 0x80
	if _, err = unpack(masked, key); err == nil {
		t.Fatal("tampered flag accepted")
	}
}
func TestTunerMeasuresMTUAndConcurrencyWithoutChangingProfile(t *testing.T) {
	p, server := testProfile(t)
	p.TimeoutMS = 150
	slots := make(chan struct{}, 4)
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, err := extract(m, p.Domain, p.Key)
		if err != nil {
			return
		}
		if f.Op == opTune {
			packet, _ := response(m, frame{Op: f.Op, Session: f.Session, Nonce: f.Nonce, Data: tuningData(f)}, p.Key).Pack()
			if len(packet) > 900 {
				return
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	p.Obfuscation = "xor"
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	report := c.FindOptimalParameters(ctx, nil)
	if err := report.ValidateApplicable(); err != nil {
		t.Fatal(err)
	}
	cfg := report.Settings[addr]
	if cfg.MTU != 900 || cfg.TXWorkers < 2 || cfg.TXWorkers > 4 || cfg.RXWorkers < 2 || cfg.RXWorkers > 4 {
		t.Fatalf("ignored measured limits: %s", report.JSON())
	}
	if len(c.Profile.ResolverSettings) != 0 {
		t.Fatal("tuner silently mutated profile")
	}
	if bytes.Contains([]byte(report.JSON()), []byte(p.Key)) {
		t.Fatal("report exposed key")
	}
}
func TestConfiguredLimitsAndXORTunnelTransfer(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.Obfuscation = "xor"
	p.ResolverSettings = map[string]ResolverSettings{}
	for _, addr := range p.ResolverPool() {
		p.ResolverSettings[addr] = ResolverSettings{MTU: 512, UploadMTU: 32, TXWorkers: 3, RXWorkers: 2}
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	data := bytes.Repeat([]byte("configured-obfuscated"), 300)
	done := make(chan error, 1)
	go func() { _, err := conn.Write(data); done <- err }()
	out := make([]byte, len(data))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !bytes.Equal(out, data) {
		t.Fatalf("transfer: %v", err)
	}
	if c.uploadSize != 32 || c.readCapacity() >= chunkSize {
		t.Fatal("saved MTU not applied")
	}
}
func TestIndependentResolverFailureRedistributesAndRecovers(t *testing.T) {
	p, server := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 300
	p.Groups = nil
	var dead atomic.Bool
	var sent atomic.Int64
	for i := 0; i < 3; i++ {
		addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
			f, _ := extract(m, p.Domain, p.Key)
			if i == 0 && f.Op == opWrite {
				sent.Add(1)
				if dead.Load() {
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
			server.ServeDNS(w, m)
		}))
		p.Groups = append(p.Groups, Group{Resolvers: []string{addr}})
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(9 * time.Second))
	data := make([]byte, 32768)
	for i := range data {
		data[i] = byte(i * 31)
	}
	done := make(chan error, 1)
	go func() { _, err := conn.Write(data); done <- err }()
	for sent.Load() < 4 {
		select {
		case <-ctx.Done():
			t.Fatal("path never used")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	dead.Store(true)
	go func() { time.Sleep(400 * time.Millisecond); dead.Store(false) }()
	out := make([]byte, len(data))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !bytes.Equal(out, data) {
		t.Fatalf("failover lost/reordered bytes: %v", err)
	}
	c.TestLatency(ctx, nil)
	if _, err := conn.Write([]byte("recovery")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	var recovered bool
	for _, h := range c.healthSnapshot() {
		if h.Endpoint == p.Groups[0].Resolvers[0] {
			recovered = h.Healthy && h.Recoveries > 0 && h.Timeouts > 0
		}
	}
	if c.Stats().Hedges != 0 {
		t.Fatal("one-resolver groups duplicated work after global admission")
	}
	if !recovered || c.Stats().Retries == 0 {
		t.Fatalf("missing failure/recovery evidence: %s", c.StatsJSON())
	}
}
func TestTuningCancellationProducesNoApplicableSettings(t *testing.T) {
	p, _ := testProfile(t)
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := c.FindOptimalParameters(ctx, nil)
	if !r.Cancelled || r.ValidateApplicable() == nil {
		t.Fatal("cancelled recommendations applicable")
	}
}

func TestBulkUploadUsesRemainingCreditBesideIdleHeadRead(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.pathLocked(addr).limit = 2
	readRelease := c.leasePathLocked(addr, opRead, false)
	if !c.pathAvailableLocked(addr, opWrite, true) {
		c.adaptiveMu.Unlock()
		readRelease()
		t.Fatal("reserved interactive slot starved remaining upload credit")
	}
	writeRelease := c.leasePathLocked(addr, opWrite, true)
	if c.pathAvailableLocked(addr, opWrite, true) {
		c.adaptiveMu.Unlock()
		readRelease()
		writeRelease()
		t.Fatal("bulk exceeded shared bound")
	}
	c.adaptiveMu.Unlock()
	writeRelease()
	readRelease()
}

func TestSelectiveACKFreesUploadWorkerBeforePrefixCompletes(t *testing.T) {
	p, server := testProfile(t)
	p.Window = 2
	p.TimeoutMS = 1000
	release := make(chan struct{})
	later := make(chan struct{}, 1)
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, _ := extract(m, p.Domain, p.Key)
		if f.Op == opWrite {
			if f.Seq == 0 {
				select {
				case <-release:
				case <-time.After(2 * time.Second):
				}
			}
			if f.Seq >= uint32(p.Window) {
				select {
				case later <- struct{}{}:
				default:
				}
			}
		}
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	data := bytes.Repeat([]byte("selective"), 300)
	done := make(chan error, 1)
	go func() { _, err := conn.Write(data); done <- err }()
	select {
	case <-later:
		close(release)
	case <-time.After(500 * time.Millisecond):
		close(release)
		t.Fatal("selective ACK could not advance worker beyond missing prefix")
	}
	out := make([]byte, len(data))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !bytes.Equal(out, data) {
		t.Fatalf("selective pipeline corrupted bytes: %v", err)
	}
}
