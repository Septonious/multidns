// Package mobile exposes a deliberately small gomobile-compatible API.
package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Septonious/multidns/core"
	"net"
	"sync"
	"time"
)

type Protector interface{ Protect(fd int) bool }
type Events interface{ OnEvent(message string) }
type Engine struct {
	mu            sync.Mutex
	client        *core.Client
	listener      net.Listener
	scanCancel    context.CancelFunc
	latencyCancel context.CancelFunc
	tuneCancel    context.CancelFunc
	tuneStopped   bool
	startCancel   context.CancelFunc
	generation    uint64
	stopped       bool
}

func NewEngine() *Engine { return &Engine{} }
func (e *Engine) NetworkChanged() {
	e.mu.Lock()
	c := e.client
	e.mu.Unlock()
	if c != nil {
		c.ResetSockets()
	}
}
func Validate(profile string) error { _, e := core.ParseProfile(profile); return e }
func (e *Engine) Start(profile string, port int, protector Protector) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return fmt.Errorf("engine stopped; create a new engine to reconnect")
	}
	if e.client != nil || e.startCancel != nil {
		e.mu.Unlock()
		return fmt.Errorf("already running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	e.startCancel = cancel
	e.generation++
	generation := e.generation
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		if e.generation == generation {
			e.startCancel = nil
		}
		e.mu.Unlock()
	}()
	p, err := core.ParseProfile(profile)
	if err != nil {
		return err
	}
	// Android uses automatic control even for profiles saved by older builds.
	p.Adaptive = true
	p.ResolverSettings = nil
	p.Window = 8
	// Startup has a deadline, but the connected client lives until Stop.
	clientCtx, clientCancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, clientCancel)
	defer stop()
	c, err := core.NewClientContext(clientCtx, p, protector)
	if err != nil {
		clientCancel()
		return err
	}
	c.SetDiagnostics(false)
	if err = c.Probe(ctx); err != nil {
		clientCancel()
		c.Close()
		return err
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		clientCancel()
		c.Close()
		return err
	}
	e.mu.Lock()
	if ctx.Err() != nil || e.generation != generation || !stop() {
		e.mu.Unlock()
		clientCancel()
		c.Close()
		l.Close()
		return context.Canceled
	}
	e.client, e.listener = c, l
	e.mu.Unlock()
	go c.ServeSOCKS(l)
	return nil
}
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stopped = true
	cancel, c, l := e.startCancel, e.client, e.listener
	latencyCancel := e.latencyCancel
	e.startCancel, e.client, e.listener = nil, nil, nil
	e.generation++
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if latencyCancel != nil {
		latencyCancel()
	}
	if l != nil {
		l.Close()
	}
	if c != nil {
		c.Close()
	}
}
func (e *Engine) Stats() string {
	e.mu.Lock()
	c := e.client
	e.mu.Unlock()
	if c == nil {
		return "{}"
	}
	return c.SummaryJSON()
}
func (e *Engine) DebugStats() string {
	e.mu.Lock()
	c := e.client
	e.mu.Unlock()
	if c == nil || !c.DiagnosticsEnabled() {
		return "{}"
	}
	return c.StatsJSON()
}
func (e *Engine) SetDiagnostics(enabled bool) {
	e.mu.Lock()
	c := e.client
	e.mu.Unlock()
	if c != nil {
		c.SetDiagnostics(enabled)
	}
}
func (e *Engine) Scan(options string, protector Protector, events Events) error {
	var o core.ScanOptions
	if err := json.Unmarshal([]byte(options), &o); err != nil {
		return err
	}
	e.mu.Lock()
	if e.scanCancel != nil {
		e.mu.Unlock()
		return fmt.Errorf("scan already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.scanCancel = cancel
	e.mu.Unlock()
	defer func() { cancel(); e.mu.Lock(); e.scanCancel = nil; e.mu.Unlock() }()
	return core.Scan(ctx, o, protector, func(r core.ScanResult) { events.OnEvent(r.JSON()) })
}
func (e *Engine) StopScan() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scanCancel != nil {
		e.scanCancel()
	}
}

func (e *Engine) TestLatency(profile string, events Events) error {
	p, err := core.ParseProfile(profile)
	if err != nil {
		return err
	}
	p.Automatic = false
	p.Adaptive = false
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		cancel()
		return context.Canceled
	}
	e.latencyCancel = cancel
	e.mu.Unlock()
	defer func() { cancel(); e.mu.Lock(); e.latencyCancel = nil; e.mu.Unlock() }()
	c, err := core.NewClientContext(ctx, p, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	c.TestLatency(ctx, func(h core.ResolverHealth) {
		if events != nil {
			b, _ := json.Marshal(h)
			events.OnEvent(string(b))
		}
	})
	return ctx.Err()
}

// FindOptimalParameters uses protected sockets when the VPN is active. Its
// result contains settings/evidence only; server credentials never enter it.
func (e *Engine) FindOptimalParameters(profile string, protector Protector, events Events) (string, error) {
	p, err := core.ParseProfile(profile)
	if err != nil {
		return "", err
	}
	p.Automatic = false
	// Ignore saved recommendations while measuring the raw path again.
	p.ResolverSettings = nil
	p.Adaptive = false
	e.mu.Lock()
	if e.tuneStopped {
		e.mu.Unlock()
		now := time.Now()
		return (core.TuningReport{Started: now, Finished: now, Cancelled: true, Settings: map[string]core.ResolverSettings{}}).JSON(), nil
	}
	if e.tuneCancel != nil {
		e.mu.Unlock()
		return "", core.ErrTuningBusy
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	e.tuneCancel = cancel
	e.mu.Unlock()
	defer func() { cancel(); e.mu.Lock(); e.tuneCancel = nil; e.mu.Unlock() }()
	c, err := core.NewClientContext(ctx, p, protector)
	if err != nil {
		return "", err
	}
	defer c.Close()
	report := c.FindOptimalParameters(ctx, func(message string) {
		if events != nil {
			events.OnEvent(message)
		}
	})
	return report.JSON(), nil
}
func (e *Engine) CancelParameterTest() {
	e.mu.Lock()
	cancel := e.tuneCancel
	e.tuneStopped = true
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func NormalizeResolver(endpoint string) (string, error) { return core.Endpoint(endpoint) }
