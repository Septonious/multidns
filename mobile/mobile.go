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
	mu          sync.Mutex
	client      *core.Client
	listener    net.Listener
	scanCancel  context.CancelFunc
	startCancel context.CancelFunc
	generation  uint64
	stopped     bool
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
	// Startup has a deadline, but the connected client lives until Stop.
	clientCtx, clientCancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, clientCancel)
	defer stop()
	c, err := core.NewClientContext(clientCtx, p, protector)
	if err != nil {
		clientCancel()
		return err
	}
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
	e.startCancel, e.client, e.listener = nil, nil, nil
	e.generation++
	e.mu.Unlock()
	if cancel != nil {
		cancel()
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
	return c.StatsJSON()
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
	c, err := core.NewClient(p, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.TestLatency(ctx, func(h core.ResolverHealth) { b, _ := json.Marshal(h); events.OnEvent(string(b)) })
	return nil
}
