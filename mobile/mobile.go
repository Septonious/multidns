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
	mu         sync.Mutex
	client     *core.Client
	listener   net.Listener
	scanCancel context.CancelFunc
}

func NewEngine() *Engine            { return &Engine{} }
func Validate(profile string) error { _, e := core.ParseProfile(profile); return e }
func (e *Engine) Start(profile string, port int, protector Protector) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		return fmt.Errorf("already running")
	}
	p, err := core.ParseProfile(profile)
	if err != nil {
		return err
	}
	c, err := core.NewClient(p, protector)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = c.Probe(ctx)
	cancel()
	if err != nil {
		c.Close()
		return err
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		c.Close()
		return err
	}
	e.client = c
	e.listener = l
	go c.ServeSOCKS(l)
	return nil
}
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		e.client.Close()
		e.listener.Close()
		e.client = nil
	}
}
func (e *Engine) Stats() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return "{}"
	}
	return e.client.StatsJSON()
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
