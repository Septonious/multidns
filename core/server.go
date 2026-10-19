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
	mu        sync.Mutex
	conn      net.Conn
	network   string
	udpBuffer []byte
	ready     chan struct{}
	notify    chan struct{}
	err       error
	nextWrite uint32
	writes    map[uint32][]byte
	chunks    map[uint32][]byte
	nextRead  uint32
	eof       bool
	closed    bool
	last      time.Time
}
type Server struct {
	Domain, Key string
	mu          sync.Mutex
	streams     map[streamKey]*serverStream
	slots       chan struct{}
}

func NewServer(domain, key string) (*Server, error) {
	p := Profile{Domain: domain, Key: key, Groups: []Group{{Resolvers: []string{"127.0.0.1"}}}}
	if e := p.Validate(); e != nil {
		return nil, e
	}
	return &Server{Domain: p.Domain, Key: key, streams: map[streamKey]*serverStream{}, slots: make(chan struct{}, 256)}, nil
}
func (s *Server) ServeDNS(w dns.ResponseWriter, m *dns.Msg) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return
	}
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
	data := s.handle(f)
	f.Data = data
	_ = w.WriteMsg(response(m, f, s.Key))
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
		st = &serverStream{ready: make(chan struct{}), notify: make(chan struct{}, 1), writes: map[uint32][]byte{}, chunks: map[uint32][]byte{}, last: time.Now()}
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
	for seq := range st.chunks {
		if seq < f.Ack {
			delete(st.chunks, seq)
		}
	}
	st.signal()
	switch f.Op {
	case opOpen:
		return []byte{statusOK}
	case opWrite:
		if st.closed {
			return []byte{statusError}
		}
		if f.Seq < st.nextWrite {
			return []byte{statusOK}
		}
		if f.Seq-st.nextWrite >= 64 {
			return []byte{statusWait}
		}
		if _, ok := st.writes[f.Seq]; !ok {
			st.writes[f.Seq] = append([]byte(nil), f.Data...)
		}
		for {
			b, ok := st.writes[st.nextWrite]
			if !ok {
				break
			}
			_ = st.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if e := st.writePayload(b); e != nil {
				st.err = e
				return []byte{statusError}
			}
			delete(st.writes, st.nextWrite)
			st.nextWrite++
		}
		return []byte{statusOK}
	case opRead:
		if b, ok := st.chunks[f.Seq]; ok {
			return append([]byte{statusOK}, b...)
		}
		if f.Seq < st.nextRead {
			return []byte{statusError}
		}
		if st.eof {
			return []byte{statusEOF}
		}
		return []byte{statusWait}
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
	select {
	case st.notify <- struct{}{}:
	default:
	}
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
	for {
		st.mu.Lock()
		full := len(st.chunks) >= 64
		closed := st.closed
		st.mu.Unlock()
		if closed {
			return
		}
		if full {
			select {
			case <-st.notify:
			case <-time.After(time.Second):
			}
			continue
		}
		b := make([]byte, chunkSize)
		if network == "udp" {
			b = make([]byte, 65535)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Minute))
		n, e := c.Read(b)
		if network == "udp" && n > 0 {
			packet := make([]byte, n+4)
			binary.BigEndian.PutUint32(packet, uint32(n))
			copy(packet[4:], b[:n])
			for len(packet) > 0 {
				size := min(chunkSize, len(packet))
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
		st.mu.Unlock()
		select {
		case <-st.notify:
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
