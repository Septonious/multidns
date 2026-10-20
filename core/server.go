package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type streamKey struct {
	session uint64
	stream  uint32
}
type serverStream struct {
	mu         sync.Mutex
	conn       net.Conn
	network    string
	udpBuffer  []byte
	ready      chan struct{}
	notify     chan struct{}
	err        error
	nextWrite  uint32
	writes     map[uint32][]byte
	chunks     map[uint32][]byte
	nextRead   uint32
	eof        bool
	closed     bool
	last       time.Time
	readSize   int
	queuedACK  bool
	writeLimit uint32
}
type Server struct {
	Domain, Key string
	mu          sync.Mutex
	streams     map[streamKey]*serverStream
	slots       chan struct{}
	readSlots   chan struct{}
	readSize    int
}

func NewServer(domain, key string) (*Server, error) {
	p := Profile{Domain: domain, Key: key, Groups: []Group{{Resolvers: []string{"127.0.0.1"}}}}
	if e := p.Validate(); e != nil {
		return nil, e
	}
	return &Server{Domain: p.Domain, Key: key, streams: map[streamKey]*serverStream{}, slots: make(chan struct{}, 256), readSlots: make(chan struct{}, 192), readSize: downloadCapacity(p)}, nil
}
func (s *Server) ServeDNS(w dns.ResponseWriter, m *dns.Msg) {
	f, e := extract(m, s.Domain, s.Key)
	if e != nil {
		r := new(dns.Msg)
		r.SetRcode(m, dns.RcodeRefused)
		_ = w.WriteMsg(r)
		return
	}
	sent := int64(f.Nonce >> 32)
	if dt := time.Now().Unix() - sent; dt > 120 || dt < -120 {
		return
	}
	// Authenticate before sending a retryable busy response. Read long-polls
	// reserve room for uploads and control traffic instead of filling every
	// worker and silently dropping subsequent requests until their timeouts.
	admitted := true
	if f.Op == opRead {
		select {
		case s.readSlots <- struct{}{}:
			defer func() { <-s.readSlots }()
		default:
			admitted = false
		}
	}
	if admitted {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			admitted = false
		}
	}
	data := []byte{statusWait}
	if admitted {
		data = s.handle(f)
	}
	f.Data = data
	reply := response(m, f, s.Key)
	if _, udp := w.RemoteAddr().(*net.UDPAddr); udp {
		limit := uint16(512)
		if opt := m.IsEdns0(); opt != nil {
			limit = max(512, min(opt.UDPSize(), 1232))
		}
		// Preserve the full immutable chunk for TCP retry instead of sending an
		// oversized datagram or changing the sequence's payload after truncation.
		reply.Truncate(int(limit))
	}
	_ = w.WriteMsg(reply)
}
func (s *Server) handle(f frame) []byte {
	if f.Op == opProbe {
		return append([]byte{statusOK}, f.Data...)
	}
	k := streamKey{f.Session, f.Stream}
	s.mu.Lock()
	st := s.streams[k]
	if st == nil && f.Op == opOpen {
		if len(s.streams) >= maxStreams {
			s.mu.Unlock()
			return []byte{statusError}
		}
		st = &serverStream{ready: make(chan struct{}), notify: make(chan struct{}), writes: map[uint32][]byte{}, chunks: map[uint32][]byte{}, last: time.Now(), readSize: s.readSize, queuedACK: f.Seq&queuedWriteACK != 0}
		if st.queuedACK && f.Seq&extendedUploadWindow != 0 {
			st.writeLimit = largeUploadWindow
		}
		s.streams[k] = st
		go st.open(f.Data)
	}
	s.mu.Unlock()
	if st == nil {
		return []byte{statusError}
	}
	select {
	case <-st.ready:
	case <-time.After(150 * time.Millisecond):
		return []byte{statusWait}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.last = time.Now()
	if st.err != nil {
		return []byte{statusError}
	}
	if f.Ack > st.nextRead {
		return []byte{statusError}
	}
	freed := false
	for seq := range st.chunks {
		if seq < f.Ack {
			delete(st.chunks, seq)
			freed = true
		}
	}
	if freed {
		st.signal()
	}
	switch f.Op {
	case opOpen:
		if st.queuedACK {
			capabilities := byte(queuedWriteACK)
			if st.writeLimit == largeUploadWindow {
				capabilities |= byte(extendedUploadWindow)
			}
			return []byte{statusOK, capabilities}
		}
		return []byte{statusOK}
	case opWrite:
		if st.closed {
			return []byte{statusError}
		}
		if f.Seq < st.nextWrite {
			return []byte{statusOK}
		}
		if f.Seq-st.nextWrite >= max(64, st.writeLimit) {
			return []byte{statusWait}
		}
		if _, ok := st.writes[f.Seq]; !ok {
			st.writes[f.Seq] = append([]byte(nil), f.Data...)
		}
		st.signal() // The writer drains the bounded, ordered queue outside st.mu.
		if st.queuedACK {
			return []byte{statusOK}
		}
		// Legacy clients receive ACKs only for committed bytes. Close after a successful Write must not
		// discard a tail still waiting in the userspace reorder queue.
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		for st.nextWrite <= f.Seq && st.err == nil && !st.closed {
			wake := st.notify
			st.mu.Unlock()
			expired := false
			select {
			case <-wake:
			case <-timer.C:
				expired = true
			}
			st.mu.Lock()
			if expired {
				return []byte{statusWait}
			}
		}
		if st.err != nil || st.closed {
			return []byte{statusError}
		}
		return []byte{statusOK}
	case opRead:
		// Wait briefly for this exact immutable chunk. Immediate WAIT replies
		// caused every idle stream/window position to poll repeatedly.
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		for {
			if st.err != nil {
				return []byte{statusError}
			}
			if b, ok := st.chunks[f.Seq]; ok {
				return append([]byte{statusOK}, b...)
			}
			if f.Seq < st.nextRead {
				return []byte{statusError}
			}
			if st.eof || st.closed {
				return []byte{statusEOF}
			}
			notify := st.notify
			st.mu.Unlock()
			expired := false
			select {
			case <-notify:
			case <-timer.C:
				expired = true
			}
			st.mu.Lock()
			if expired {
				return []byte{statusWait}
			}
		}
	case opFlush:
		if !st.queuedACK {
			return []byte{statusError}
		}
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		for st.nextWrite < f.Seq && st.err == nil && !st.closed {
			wake := st.notify
			st.mu.Unlock()
			expired := false
			select {
			case <-wake:
			case <-timer.C:
				expired = true
			}
			st.mu.Lock()
			if expired {
				return []byte{statusWait}
			}
		}
		if st.err != nil || st.closed {
			return []byte{statusError}
		}
		return []byte{statusOK}
	case opHalfClose:
		if f.Seq != st.nextWrite {
			return []byte{statusWait}
		}
		if tcp, ok := st.conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		return []byte{statusOK}
	case opClose:
		st.closed = true
		_ = st.conn.Close()
		st.signal()
		return []byte{statusOK}
	default:
		return []byte{statusError}
	}
}
func (st *serverStream) signal() {
	// Called with st.mu held. Wake all readers waiting for different sequences
	// as well as the producer waiting for an acknowledged buffer slot.
	close(st.notify)
	st.notify = make(chan struct{})
}
func (st *serverStream) open(data []byte) {
	target := string(data)
	network := "tcp"
	if strings.HasPrefix(target, "udp:") {
		network = "udp"
		target = strings.TrimPrefix(target, "udp:")
	}
	_, port, e := net.SplitHostPort(target)
	if e == nil && port == "0" {
		e = fmt.Errorf("invalid port")
	}
	var c net.Conn
	if e == nil {
		c, e = net.DialTimeout(network, target, 10*time.Second)
	}
	st.mu.Lock()
	st.conn = c
	st.network = network
	st.err = e
	close(st.ready)
	st.mu.Unlock()
	if e != nil {
		return
	}
	go st.flushWrites()
	for {
		st.mu.Lock()
		full := len(st.chunks) >= 64
		closed := st.closed
		notify := st.notify
		st.mu.Unlock()
		if closed {
			return
		}
		if full {
			select {
			case <-notify:
			case <-time.After(time.Second):
			}
			continue
		}
		b := make([]byte, st.readSize)
		if network == "udp" {
			b = make([]byte, 65535)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Minute))
		n, e := c.Read(b)
		if network == "tcp" && n > 0 && n < len(b) && e == nil {
			// Tiny destination writes otherwise consume one entire DNS chunk and
			// receive-credit slot each. Briefly coalesce them without waiting for
			// a full chunk on interactive traffic (at most 2 ms added latency).
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Millisecond))
			for n < len(b) {
				more, err := c.Read(b[n:])
				n += more
				if err != nil {
					if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
						e = err
					}
					break
				}
				if more == 0 {
					break
				}
			}
		}
		if network == "udp" && n > 0 {
			packet := make([]byte, n+4)
			binary.BigEndian.PutUint32(packet, uint32(n))
			copy(packet[4:], b[:n])
			for len(packet) > 0 {
				size := min(st.readSize, len(packet))
				st.appendChunk(packet[:size])
				packet = packet[size:]
			}
		} else if n > 0 {
			st.appendChunk(b[:n])
		}
		if e != nil {
			st.mu.Lock()
			st.eof = true
			st.signal()
			st.mu.Unlock()
			return
		}
	}
}

// A slow destination must not hold the stream mutex (or hundreds of DNS
// handlers) while its TCP send buffer is full. The queue is bounded and
// negotiated queued ACKs are paired with a delivery barrier before Write returns.
func (st *serverStream) flushWrites() {
	for {
		st.mu.Lock()
		if st.closed || st.err != nil {
			st.mu.Unlock()
			return
		}
		start, end := st.nextWrite, st.nextWrite
		var payload []byte
		for {
			b, ok := st.writes[end]
			if !ok {
				break
			}
			payload = append(payload, b...)
			end++
		}
		wake := st.notify
		st.mu.Unlock()
		if start == end {
			<-wake
			continue
		}
		_ = st.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		err := st.writePayload(payload)
		st.mu.Lock()
		if err != nil {
			st.err = err
			st.signal()
			st.mu.Unlock()
			return
		}
		for st.nextWrite < end {
			delete(st.writes, st.nextWrite)
			st.nextWrite++
		}
		st.signal()
		st.mu.Unlock()
	}
}

func (st *serverStream) appendChunk(b []byte) {
	for {
		st.mu.Lock()
		if st.closed {
			st.mu.Unlock()
			return
		}
		if len(st.chunks) < 64 {
			st.chunks[st.nextRead] = append([]byte(nil), b...)
			st.nextRead++
			st.signal()
			st.mu.Unlock()
			return
		}
		notify := st.notify
		st.mu.Unlock()
		select {
		case <-notify:
		case <-time.After(time.Second):
		}
	}
}
func (s *Server) Cleanup(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, st := range s.streams {
				st.mu.Lock()
				if st.conn != nil {
					st.conn.Close()
				}
				st.closed = true
				st.signal()
				st.mu.Unlock()
			}
			s.mu.Unlock()
			return
		case <-t.C:
			s.mu.Lock()
			for k, st := range s.streams {
				st.mu.Lock()
				if time.Since(st.last) > 5*time.Minute {
					if st.conn != nil {
						st.conn.Close()
					}
					st.closed = true
					st.signal()
					delete(s.streams, k)
				}
				st.mu.Unlock()
			}
			s.mu.Unlock()
		}
	}
}
func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func (st *serverStream) writePayload(b []byte) error {
	if st.network != "udp" {
		return writeFull(st.conn, b)
	}
	st.udpBuffer = append(st.udpBuffer, b...)
	for len(st.udpBuffer) >= 4 {
		n := int(binary.BigEndian.Uint32(st.udpBuffer[:4]))
		if n > 65507 {
			return fmt.Errorf("invalid datagram length")
		}
		if len(st.udpBuffer) < 4+n {
			break
		}
		if _, e := st.conn.Write(st.udpBuffer[4 : 4+n]); e != nil {
			return e
		}
		st.udpBuffer = st.udpBuffer[4+n:]
	}
	return nil
}
