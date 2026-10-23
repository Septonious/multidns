package core

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type Protector interface{ Protect(fd int) bool }
type Stats struct {
	LateReplies         uint64  `json:"late_replies"`
	AbandonedTimeouts   uint64  `json:"abandoned_timeouts"`
	ReadAdmissionBudget int     `json:"read_admission_budget"`
	ReadBudget          int     `json:"read_budget"`
	PeerReadWindow      uint32  `json:"peer_read_window"`
	SessionSeconds      float64 `json:"session_seconds"`
	DataBudget          int     `json:"data_budget"`
	DataInFlight        int     `json:"data_in_flight"`
	ReadInFlight        int     `json:"read_in_flight"`
	ActiveUploads       int64   `json:"active_uploads"`

	UploadPayloadBytes int    `json:"upload_payload_bytes"`
	LoadedRTTLimitMS   int    `json:"loaded_rtt_limit_ms"`
	Exchanges          uint64 `json:"exchanges"`
	Timeouts           uint64 `json:"timeouts"`
	DNSErrors          uint64 `json:"dns_errors"`
	InvalidReplies     uint64 `json:"invalid_replies"`
	SocketErrors       uint64 `json:"socket_errors"`
	NetworkChanges     uint64 `json:"network_changes"`
	TransportVersion   string `json:"transport_version"`
	PeerUploadWindow   uint32 `json:"peer_upload_window"`
	Hedges             uint64 `json:"hedges"`
	DataHedges         uint64 `json:"data_hedges"`
	ControlHedges      uint64 `json:"control_hedges"`

	Queries    uint64 `json:"queries"`
	Replies    uint64 `json:"replies"`
	Retries    uint64 `json:"retries"`
	Waits      uint64 `json:"waits"`
	Uploaded   uint64 `json:"uploaded"`
	Downloaded uint64 `json:"downloaded"`
	Streams    int64  `json:"streams"`
}
type Client struct {
	lateReplies             atomic.Uint64
	abandonedTimeouts       atomic.Uint64
	rxBudget                atomic.Int64
	rxControl               readBudgetControl
	peerReadWindow          atomic.Uint32
	diagnostics             atomic.Bool
	started                 time.Time
	leasedData, leasedReads int

	dataHedges, controlHedges atomic.Uint64
	flowFlight                map[flowKey]int
	activeReaders             map[uint32]time.Time
	activeUploads             atomic.Int64

	admissionTimer       *time.Timer
	admissionChoice      *admission
	admissionChoiceAt    time.Time
	admissionChoiceValid bool

	socketsMu    sync.Mutex
	sockets      map[string]*resolverSocket
	socketsEpoch uint64

	adaptiveMu       sync.Mutex
	groups           []Group
	health           map[string]*ResolverHealth
	paths            map[string]*pathState
	spares           []string
	deficits         []float64
	pathDeficits     map[string]float64
	dispatchDeficits map[string]float64
	waitingWrites    int
	admissions       []*admission
	socketOpened     map[string]bool

	namesMu sync.RWMutex
	names   map[string]nameEntry
	proxies sync.Map

	Profile                                                                      Profile
	session                                                                      uint64
	next                                                                         atomic.Uint32
	cursor                                                                       atomic.Uint64
	queries, replies, retries, waits, uploaded, downloaded                       atomic.Uint64
	streams                                                                      atomic.Int64
	exchanges, timeouts, dnsErrors, invalidReplies, socketErrors, networkChanges atomic.Uint64
	hedges                                                                       atomic.Uint64
	peerUploadWindow                                                             atomic.Uint32

	dialer     *net.Dialer
	ctx        context.Context
	cancel     context.CancelFunc
	slots      chan struct{}
	readSlots  chan struct{}
	dataSlots  chan struct{}
	uploadSize int
}

func NewClient(p Profile, protector Protector) (*Client, error) {
	return NewClientContext(context.Background(), p, protector)
}

// NewClientContext permits cancellation even during automatic resolver discovery.
func NewClientContext(parent context.Context, p Profile, protector Protector) (*Client, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(parent)
	globalBudget, readBudget, dataBudget := 128, 96, 120
	if p.Adaptive {
		// Only independent path credits grow: idle polls retain their global cap.
		// A large pool must not inherit the same TX ceiling as four groups.
		dataBudget = min(512, max(240, 64*len(p.ResolverPool())))
		globalBudget, readBudget = dataBudget+16, 320
	}
	c := &Client{started: time.Now(), Profile: p, dialer: &net.Dialer{Timeout: 10 * time.Second}, ctx: ctx, cancel: cancel, slots: make(chan struct{}, globalBudget), names: map[string]nameEntry{}, sockets: map[string]*resolverSocket{}}
	// Idle download polls must leave capacity for uploads and control requests.
	c.readSlots = make(chan struct{}, readBudget)
	c.rxBudget.Store(int64(min(readBudget, 160)))
	c.dataSlots = make(chan struct{}, dataBudget)
	c.socketOpened = map[string]bool{}
	c.diagnostics.Store(true)
	c.uploadSize = uploadCapacity(p)
	for _, settings := range p.ResolverSettings {
		if settings.UploadMTU > 0 {
			c.uploadSize = min(c.uploadSize, settings.UploadMTU)
		}
	}
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		cancel()
		return nil, e
	}
	c.session = binary.BigEndian.Uint64(b[:])
	if protector != nil {
		c.dialer.Control = func(_, _ string, raw syscall.RawConn) error {
			var e error
			e2 := raw.Control(func(fd uintptr) {
				if !protector.Protect(int(fd)) {
					e = fmt.Errorf("socket protection failed")
				}
			})
			if e2 != nil {
				return e2
			}
			return e
		}
	}
	c.groups = append([]Group(nil), p.Groups...)
	c.health = map[string]*ResolverHealth{}
	if p.Automatic {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
		c.TestLatency(ctx, nil)
		cancel()
		if err := c.ctx.Err(); err != nil {
			c.Close()
			return nil, err
		}
		if !c.regroup() {
			c.Close()
			return nil, fmt.Errorf("no healthy resolvers passed startup checks")
		}
	}
	if p.Adaptive || p.Automatic {
		go c.maintain()
	}
	return c, nil
}
func (c *Client) Close() {
	c.cancel()
	c.socketsMu.Lock()
	for _, socket := range c.sockets {
		socket.close()
	}
	c.socketsMu.Unlock()
	c.proxies.Range(func(key, value any) bool { key.(net.Conn).Close(); return true })
}
func (c *Client) Stats() Stats {
	return Stats{LateReplies: c.lateReplies.Load(), AbandonedTimeouts: c.abandonedTimeouts.Load(), ReadBudget: c.readBudget(), ReadAdmissionBudget: c.readAdmissionBudget(), PeerReadWindow: c.peerReadWindow.Load(), SessionSeconds: time.Since(c.started).Seconds(), DataBudget: cap(c.dataSlots), DataInFlight: len(c.dataSlots), ReadInFlight: len(c.readSlots), ActiveUploads: c.activeUploads.Load(), UploadPayloadBytes: c.uploadSize, LoadedRTTLimitMS: c.Profile.LoadedRTTLimitMS, TransportVersion: "0.3.8", PeerUploadWindow: c.peerUploadWindow.Load(), Hedges: c.hedges.Load(), DataHedges: c.dataHedges.Load(), ControlHedges: c.controlHedges.Load(), Exchanges: c.exchanges.Load(), Timeouts: c.timeouts.Load(), DNSErrors: c.dnsErrors.Load(), InvalidReplies: c.invalidReplies.Load(), SocketErrors: c.socketErrors.Load(), NetworkChanges: c.networkChanges.Load(), Queries: c.queries.Load(), Replies: c.replies.Load(), Retries: c.retries.Load(), Waits: c.waits.Load(), Uploaded: c.uploaded.Load(), Downloaded: c.downloaded.Load(), Streams: c.streams.Load()}
}
func (c *Client) StatsJSON() string {
	c.adaptiveMu.Lock()
	spares := append([]string(nil), c.spares...)
	queued := len(c.admissions)
	oldestMS := 0.0
	for _, a := range c.admissions {
		oldestMS = max(oldestMS, float64(time.Since(a.started).Microseconds())/1000)
	}
	c.adaptiveMu.Unlock()
	b, _ := json.Marshal(struct {
		Stats
		Groups    []Group          `json:"groups"`
		Resolvers []ResolverHealth `json:"resolvers"`
		Spares    []string         `json:"spares"`
		Queued    int              `json:"admission_queued"`
		OldestMS  float64          `json:"oldest_admission_ms"`
	}{c.Stats(), c.groupsSnapshot(), c.healthSnapshot(), spares, queued, oldestMS})
	return string(b)
}
func freshNonce() uint64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return uint64(time.Now().Unix())<<32 | uint64(binary.BigEndian.Uint32(b[:]))
}
func (c *Client) race(ctx context.Context, f frame, group int) (frame, error) {
	f.Session = c.session
	f.Nonce = freshNonce()
	m, e := query(f, c.Profile)
	if e != nil {
		return frame{}, e
	}
	c.exchanges.Add(1)
	type result struct {
		f frame
		e error
	}
	groups := c.groupsSnapshot()
	targets := c.raceTargets(groups[group%len(groups)], f.Bulk)
	hedgeDelay := time.Duration(0)
	if c.Profile.Adaptive && f.Op != opRead && f.Op != opWrite {
		targets, hedgeDelay = c.controlTargets(targets)
	}
	var primaryRelease func()
	if c.Profile.Adaptive && (f.Op == opRead || f.Op == opWrite) {
		var primary string
		primary, primaryRelease, e = c.acquireAnyPath(ctx, targets, f.Op, f.Bulk, f.Stream)
		if e != nil {
			return frame{}, e
		}
		// A backup comes from the live pool at send time. Group shape must not
		// decide whether an ordered stream can escape a lost primary packet.
		targets = []string{primary, ""}
		hedgeDelay = c.hedgeDelay(primary, f.Op, f.Bulk)
	}
	backupNow := make(chan struct{})
	var startBackups sync.Once
	results := make(chan result, len(targets))
	raceCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	for index, addr := range targets {
		go func(addr string, index int) {
			if index > 0 && hedgeDelay > 0 {
				timer := time.NewTimer(hedgeDelay)
				select {
				case <-timer.C:
				case <-backupNow:
				case <-raceCtx.Done():
					timer.Stop()
					results <- result{e: raceCtx.Err()}
					return
				}
				timer.Stop()
			}
			bulk := f.Bulk
			if index > 0 && f.Op == opWrite && f.uploadHead != nil && f.uploadHead() {
				// Only the missing prefix may use foreground reserve. Later
				// accepted chunks cannot drain the reorder queue without it.
				bulk = false
			}
			var release func()
			var err error
			if index == 0 && primaryRelease != nil {
				release = primaryRelease
			} else if index > 0 && primaryRelease != nil {
				addr, release, err = c.tryAlternative(targets[0], f.Op, f.Stream, bulk)
			} else {
				release, err = c.acquirePath(raceCtx, addr, f.Op)
			}
			if err != nil {
				results <- result{e: err}
				return
			}
			admittedAt := time.Now()
			wireDone := finishedDNS
			releases := []func(){release}
			defer func() {
				if !c.Profile.Adaptive {
					wireDone = finishedDNS
				}
				releaseAll := func() {
					for i := len(releases) - 1; i >= 0; i-- {
						releases[i]()
					}
				}
				select {
				case <-wireDone:
					releaseAll()
				default:
					go func() {
						select {
						case <-wireDone:
						case <-c.ctx.Done():
						}
						releaseAll()
					}()
				}
			}()
			if f.Op == opRead {
				select {
				case c.readSlots <- struct{}{}:
					releases = append(releases, func() { <-c.readSlots })
				case <-raceCtx.Done():
					results <- result{e: raceCtx.Err()}
					return
				}
			}
			if f.Op == opRead || f.Op == opWrite {
				select {
				case c.dataSlots <- struct{}{}:
					releases = append(releases, func() { <-c.dataSlots })
				case <-raceCtx.Done():
					results <- result{e: raceCtx.Err()}
					return
				}
			}
			select {
			case c.slots <- struct{}{}:
				releases = append(releases, func() { <-c.slots })
			case <-raceCtx.Done():
				results <- result{e: raceCtx.Err()}
				return
			}
			if (f.Op == opWrite || f.Op == opRead) && bulk {
				if err := c.paceData(raceCtx, addr, f.Op); err != nil {
					results <- result{e: err}
					return
				}
			}
			c.recordStart(addr)
			c.adaptiveMu.Lock()
			if h := c.health[addr]; h != nil {
				waitMS := 0.0
				if c.diagnostics.Load() {
					waitMS = float64(time.Since(admittedAt).Microseconds()) / 1000
				}
				h.SlotWaitMS = .875*h.SlotWaitMS + .125*waitMS
				h.MaxSlotWaitMS = max(h.MaxSlotWaitMS, waitMS)
				if f.Op == opWrite {
					h.TXQueries++
				} else if f.Op == opRead {
					h.RXQueries++
				}
				if f.Retry {
					h.Retransmissions++
				}
				if index > 0 && hedgeDelay > 0 {
					h.Hedges++
				}
			}
			c.adaptiveMu.Unlock()
			c.queries.Add(1)
			if index > 0 && hedgeDelay > 0 {
				c.hedges.Add(1)
				if f.Op == opRead || f.Op == opWrite {
					c.dataHedges.Add(1)
				} else {
					c.controlHedges.Add(1)
				}
			}
			attempt := c.exchangePathDNS(raceCtx, m, addr, f.Op)
			r, err := attempt.message, attempt.err
			wireDone = attempt.done
			var answer frame
			if err == nil && r.Rcode != dns.RcodeSuccess {
				err = dnsFailure{r.Rcode}
			}
			if err == nil {
				answer, err = parseResponse(r, c.Profile.Key)
				if err != nil {
					err = fmt.Errorf("%w: %v", errInvalidReply, err)
				}
				if err == nil && (answer.Session != f.Session || answer.Stream != f.Stream || answer.Seq != f.Seq || answer.Nonce != f.Nonce || answer.Op != f.Op || len(answer.Data) == 0) {
					err = fmt.Errorf("%w: mismatched response", errInvalidReply)
				}
			}
			size := 0
			if err == nil {
				c.recordTransportSuccess(addr, attempt)
			}
			if err == nil && answer.Data[0] == statusOK {
				if f.Op == opRead {
					size = len(answer.Data) - 1
				} else if f.Op == opWrite {
					size = len(f.Data)
				}
			}
			// A tiny WAIT is not evidence that this path can carry large replies.
			if attempt.fallback && f.Op == opRead && (err != nil || size > 0) {
				c.recordTCPFallback(addr, err, raceCtx.Err() != nil)
			}
			if err != nil && raceCtx.Err() == nil {
				c.recordFailure(err)
			}
			c.recordEnd(addr, attempt.elapsed, size, f.Op, err, raceCtx.Err() != nil, attempt.started)
			if err == nil {
				c.replies.Add(1)
			}
			if index == 0 && err != nil {
				startBackups.Do(func() { close(backupNow) })
			}
			results <- result{answer, err}
		}(addr, index)
	}
	var last error
	for range targets {
		r := <-results
		if r.e == nil {
			return r.f, nil
		}
		last = r.e
	}
	return frame{}, last
}
func (c *Client) rpc(ctx context.Context, f frame, base int) ([]byte, error) {
	return c.rpcACK(ctx, f, base, nil)
}

var errReadPending = errors.New("download chunk not ready")

func (c *Client) rpcACK(ctx context.Context, f frame, base int, ack func() uint32, waitOnce ...bool) ([]byte, error) {
	delay := 20 * time.Millisecond
	idleWaits := 0
	group := base
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, net.ErrClosed
		default:
		}
		if ack != nil {
			f.Ack = ack()
		}
		var wake <-chan struct{}
		if f.wake != nil {
			wake = f.wake()
		}
		answer, e := c.race(ctx, f, group)
		f.Retry = false
		if e == nil {
			switch answer.Data[0] {
			case statusOK:
				return answer.Data[1:], nil
			case statusEOF:
				return nil, io.EOF
			case statusError:
				return nil, fmt.Errorf("server rejected operation %d", f.Op)
			case statusWait:
				c.waits.Add(1)
				if len(waitOnce) > 0 && waitOnce[0] {
					return nil, errReadPending
				}
				if f.Op == opRead {
					delay = 20 * time.Millisecond
					if c.Profile.Adaptive {
						idleWaits++
						delay = time.Duration(20<<min(3, idleWaits-1)) * time.Millisecond
					}
					// Idle head reads live for minutes. Re-evaluate their
					// placement instead of pinning them to yesterday's best path.
					if c.Profile.Adaptive {
						group = c.chooseGroup(group, f.Bulk, f.Op)
					}
				}
			}
		} else {
			if ctx.Err() != nil || c.ctx.Err() != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, net.ErrClosed
			}
			c.retries.Add(1)
			f.Retry = true
			group = c.chooseGroup(group+1, f.Bulk, f.Op)
			delay = time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-c.ctx.Done():
			timer.Stop()
			return nil, net.ErrClosed
		case <-timer.C:
		case <-wake:
			timer.Stop()
			idleWaits = 0
			delay = 20 * time.Millisecond
		}
		delay = min(delay*2, 200*time.Millisecond)
	}
}
func (c *Client) Probe(ctx context.Context) error {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	out, e := c.rpc(ctx, frame{Op: opProbe, Data: b}, 0)
	if e == nil && string(out) != string(b) {
		e = fmt.Errorf("probe mismatch")
	}
	return e
}
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portText, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	var port int
	fmt.Sscanf(portText, "%d", &port)
	switch c.route(host, port, network) {
	case "block":
		return nil, fmt.Errorf("blocked by routing rule")
	case "direct":
		return c.dialer.DialContext(ctx, network, address)
	}
	if network != "tcp" && network != "udp" {
		return nil, fmt.Errorf("unsupported network")
	}
	id := c.next.Add(1)
	if id == 0 {
		return nil, fmt.Errorf("stream identifiers exhausted")
	}
	base := int(c.cursor.Add(1) % uint64(len(c.groupsSnapshot())))
	data := []byte(address)
	if network == "udp" {
		data = []byte("udp:" + address)
	}
	opened, e := c.rpc(ctx, frame{Op: opOpen, Stream: id, Seq: queuedWriteACK | extendedUploadWindow | extendedReadWindow | uint32(c.readCapacity())<<8, Data: data}, base)
	if e != nil {
		return nil, e
	}
	streamCtx, cancel := context.WithCancel(c.ctx)
	s := &Conn{client: c, id: id, base: base, ctx: streamCtx, cancel: cancel, queuedACK: len(opened) == 1 && opened[0]&byte(queuedWriteACK) != 0}
	s.writeLimit = 64
	s.readLimit = 64
	if len(opened) == 1 && opened[0]&byte(extendedReadWindow) != 0 {
		s.readLimit = largeReadWindow
	}
	for old := c.peerReadWindow.Load(); uint32(s.readLimit) > old; old = c.peerReadWindow.Load() {
		if c.peerReadWindow.CompareAndSwap(old, uint32(s.readLimit)) {
			break
		}
	}
	if s.queuedACK && opened[0]&byte(extendedUploadWindow) != 0 {
		s.writeLimit = largeUploadWindow
	}
	for old := c.peerUploadWindow.Load(); uint32(s.writeLimit) > old; old = c.peerUploadWindow.Load() {
		if c.peerUploadWindow.CompareAndSwap(old, uint32(s.writeLimit)) {
			break
		}
	}
	s.readWake = make(chan struct{}, 1)
	s.reads = make(chan readResult, c.Profile.Window)
	s.deadlineMu.Lock()
	s.readDeadline = time.Now().Add(5 * time.Minute)
	s.writeDeadline = s.readDeadline
	s.deadlineMu.Unlock()
	c.streams.Add(1)
	go s.prefetch()
	if network == "udp" {
		return &datagramConn{Conn: s}, nil
	}
	return s, nil
}

type readResult struct {
	b []byte
	e error
}
type Conn struct {
	activityMu sync.Mutex
	activity   chan struct{}

	queuedACK  bool
	writeLimit int
	readLimit  int
	readWake   chan struct{}
	traffic    atomic.Uint64

	client                      *Client
	id                          uint32
	base                        int
	ctx                         context.Context
	cancel                      context.CancelFunc
	readMu, writeMu, deadlineMu sync.Mutex
	readDeadline, writeDeadline time.Time
	readSeq                     atomic.Uint32
	writeSeq                    uint32
	reads                       chan readResult
	buffer                      []byte
	closeOnce                   sync.Once
}

func (s *Conn) group(seq uint32, op byte) int {
	base := s.base
	if s.client.Profile.Adaptive {
		return base // shared admission and global backups choose the actual path
	}
	if s.client.Profile.Mode != "affinity" {
		base += int(seq)
	}
	return s.client.chooseGroup(base, s.traffic.Load() >= 4096, op)
}

// window shares sustained capacity between active flows, within each stream's
// negotiated reorder limit and the client's shared physical request budget.
func (s *Conn) window(operation ...byte) int {
	base := s.client.Profile.Window
	if !s.client.Profile.Adaptive {
		return base
	}
	op := byte(opRead)
	if len(operation) > 0 {
		op = operation[0]
	}
	ceiling := max(64, s.readLimit)
	if op == opWrite {
		ceiling = max(64, s.writeLimit)
	}
	traffic := s.traffic.Load()
	desired := base
	if traffic >= 4096 {
		desired = min(ceiling, base<<min(5, int(traffic/4096)))
	}
	capacity := max(1, s.client.dataCapacity(op))
	peers := 1
	if op == opWrite {
		peers = max(1, int(s.client.activeUploads.Load()))
	} else {
		peers = s.client.readerCount()
	}
	// Share a pool window across active flows, with two refill positions per
	// flow. Previously every stream independently offered the entire pool budget.
	share := (capacity+peers-1)/peers + 2
	return max(1, min(ceiling, min(desired, share)))

}

func (s *Conn) opContext(read bool) (context.Context, context.CancelFunc) {
	s.deadlineMu.Lock()
	d := s.writeDeadline
	if read {
		d = s.readDeadline
	}
	s.deadlineMu.Unlock()
	if d.IsZero() {
		return context.WithCancel(s.ctx)
	}
	return context.WithDeadline(s.ctx, d)
}
func (s *Conn) prefetch() {
	defer close(s.reads)
	type completion struct {
		seq, epoch uint32
		readResult
	}
	readLimit := max(64, s.readLimit)
	results := make(chan completion, readLimit)
	pending := make(map[uint32]readResult, readLimit)
	active := make(map[uint32]bool, readLimit)
	waiting := make(map[uint32]uint32, readLimit)
	delivered, window := uint32(0), uint32(1)
	launch := func(seq, epoch uint32) {
		active[seq] = true
		go func() {
			ctx, cancel := s.opContext(true)
			defer cancel()
			// Only the head of an idle stream keeps polling. Speculative future
			// chunks wait for progress before trying again, avoiding N*window
			// background polls on HTTP keepalive and upload-only connections.
			requestCtx := ctx
			if seq != epoch {
				// Speculation must not outlive a quiet transfer for seconds
				// while waiting in admission or retrying a lost future chunk.
				limited, stop := context.WithTimeout(ctx, 350*time.Millisecond)
				defer stop()
				requestCtx = limited
			}
			b, err := s.client.rpcACK(requestCtx, frame{Op: opRead, Stream: s.id, Seq: seq,
				Bulk: s.traffic.Load() >= 4096 && seq != epoch, wake: s.activityChannel}, s.group(seq, opRead), s.readSeq.Load, seq != epoch)
			if seq != epoch && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				err = errReadPending
			}
			results <- completion{seq, epoch, readResult{b, err}}
		}()
	}
	for {
		if window > 1 {
			window = uint32(s.window())
		}
		for seq := delivered; seq-delivered < window && seq-s.readSeq.Load() < uint32(readLimit); seq++ {
			if active[seq] {
				continue
			}
			if _, ok := pending[seq]; ok {
				continue
			}
			if epoch, ok := waiting[seq]; ok && epoch == delivered && seq != delivered {
				continue
			}
			launch(seq, delivered)
		}
		select {
		case <-s.readWake:
		case result := <-results:
			delete(active, result.seq)
			if errors.Is(result.e, errReadPending) {
				waiting[result.seq] = result.epoch
			} else {
				delete(waiting, result.seq)
				pending[result.seq] = result.readResult
				if result.e == nil && len(result.b) > 0 {
					// Small interactive replies do not consume a sustained bulk
					// reader's equal share for the following second.
					if len(result.b) >= chunkSize/2 {
						s.client.markReader(s.id)
					}
					window = uint32(s.window())
				}
			}
		case <-s.ctx.Done():
			return
		}
		for {
			result, ok := pending[delivered]
			if !ok {
				break
			}
			select {
			case s.reads <- result:
			case <-s.ctx.Done():
				return
			}
			delete(pending, delivered)
			delete(waiting, delivered)
			delivered++
			if result.e != nil {
				return
			}
		}
	}
}
func (s *Conn) Read(b []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	if len(s.buffer) == 0 {
		ctx, cancel := s.opContext(true)
		defer cancel()
		select {
		case r, ok := <-s.reads:
			if !ok {
				return 0, io.EOF
			}
			if r.e != nil {
				return 0, r.e
			}
			s.buffer = r.b
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	n := copy(b, s.buffer)
	s.buffer = s.buffer[n:]
	if len(s.buffer) == 0 {
		s.readSeq.Add(1)
		select {
		case s.readWake <- struct{}{}:
		default:
		}
	}
	s.client.downloaded.Add(uint64(n))
	s.traffic.Add(uint64(n))
	return n, nil
}
func (s *Conn) Write(b []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	if len(b) > 1024 {
		s.client.activeUploads.Add(1)
		defer s.client.activeUploads.Add(-1)
	}
	s.notifyActivity()
	defer s.notifyActivity()
	capacity := s.client.uploadSize
	if capacity < 8 {
		return 0, fmt.Errorf("domain leaves insufficient upload capacity")
	}
	ctx, cancel := s.opContext(false)
	defer cancel()
	type completion struct {
		index, size int
		err         error
	}
	window := s.window(opWrite)
	results := make(chan completion, largeUploadWindow)
	pending := make(map[int]completion, window)
	var uploadHead atomic.Uint32
	uploadHead.Store(s.writeSeq)
	next, acknowledged, completed, offset, total := 0, 0, 0, 0, 0
	for offset < len(b) || acknowledged < next {
		window = s.window(opWrite)
		// A selective ACK frees a worker even when an earlier chunk is still
		// on a slower path. Retain the independent negotiated reorder bound.
		for offset < len(b) && next-completed < window && next-acknowledged < max(64, s.writeLimit) {
			size := min(capacity, len(b)-offset)
			data := append([]byte(nil), b[offset:offset+size]...)
			index, seq := next, s.writeSeq
			s.writeSeq++
			offset += size
			next++
			go func() {
				_, err := s.client.rpcACK(ctx, frame{Op: opWrite, Stream: s.id, Seq: seq, Data: data, Bulk: len(b) > 1024 && s.traffic.Load() >= 4096, uploadHead: func() bool { return uploadHead.Load() == seq }}, s.group(seq, opWrite), s.readSeq.Load)
				results <- completion{index, size, err}
			}()
		}
		select {
		case result := <-results:
			completed++
			pending[result.index] = result
		case <-ctx.Done():
			return total, ctx.Err()
		}
		for {
			result, ok := pending[acknowledged]
			if !ok {
				break
			}
			if result.err != nil {
				return total, result.err
			}
			delete(pending, acknowledged)
			acknowledged++
			uploadHead.Add(1)
			total += result.size
			s.client.uploaded.Add(uint64(result.size))
			s.traffic.Add(uint64(result.size))
		}
	}
	if s.queuedACK {
		// Selective acceptance allows the window to pass a missing packet, but
		// a successful Write still guarantees the full prefix left the server's
		// userspace queue. The barrier is never sent to a legacy server.
		if _, err := s.client.rpcACK(ctx, frame{Op: opFlush, Stream: s.id, Seq: s.writeSeq}, s.base, s.readSeq.Load); err != nil {
			return total, err
		}
	}
	return total, nil
}
func (s *Conn) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	_, e := s.client.rpc(ctx, frame{Op: opHalfClose, Stream: s.id, Seq: s.writeSeq, Ack: s.readSeq.Load()}, s.base)
	return e
}
func (s *Conn) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.client.forgetReader(s.id)
		s.client.streams.Add(-1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = s.client.rpc(ctx, frame{Op: opClose, Stream: s.id, Ack: s.readSeq.Load()}, s.base)
		}()
	})
	return nil
}
func (s *Conn) LocalAddr() net.Addr  { return &net.TCPAddr{IP: net.IPv4zero} }
func (s *Conn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }
func (s *Conn) SetDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	s.writeDeadline = t
	s.deadlineMu.Unlock()
	return nil
}
func (s *Conn) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	s.deadlineMu.Unlock()
	return nil
}
func (s *Conn) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.writeDeadline = t
	s.deadlineMu.Unlock()
	return nil
}

func (s *Conn) activityChannel() <-chan struct{} {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.activity == nil {
		s.activity = make(chan struct{})
	}
	return s.activity
}
func (s *Conn) notifyActivity() {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.activity != nil {
		close(s.activity)
	}
	s.activity = make(chan struct{})
}
