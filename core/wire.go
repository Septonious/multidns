package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

const (
	opProbe byte = iota + 1
	opOpen
	opWrite
	opRead
	opClose
	opHalfClose
	opDatagram
	statusOK    byte = 0
	statusWait  byte = 1
	statusEOF   byte = 2
	statusError byte = 3
	chunkSize        = 768
	headerSize       = 30
	maxStreams       = 1024
)

// Optional v1 extension negotiated in opOpen.Seq and its response payload.
const (
	opFlush        byte   = 8
	queuedWriteACK uint32 = 1
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Find the largest upload that fits the actual encoded QNAME, including label
// separators and the authentication header. This is calculated once per client.
func uploadCapacity(p Profile) int {
	lo, hi := 0, 160
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, err := query(frame{Op: opWrite, Data: make([]byte, mid)}, p); err == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

func downloadCapacity(p Profile) int {
	request, _ := query(frame{Op: opRead}, p)
	lo, hi := 0, chunkSize
	for lo < hi {
		mid := (lo + hi + 1) / 2
		reply := response(request, frame{Op: opRead, Data: make([]byte, mid+1)}, p.Key)
		packet, err := reply.Pack()
		if err == nil && len(packet) <= 1232 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

type frame struct {
	Bulk    bool // local scheduler hint; never serialized
	Op      byte
	Session uint64
	Stream  uint32
	Seq     uint32
	Ack     uint32
	Nonce   uint64
	Data    []byte
}

func (f frame) pack(key string) []byte {
	b := make([]byte, headerSize+len(f.Data)+8)
	b[0] = 1
	b[1] = f.Op
	binary.BigEndian.PutUint64(b[2:10], f.Session)
	binary.BigEndian.PutUint32(b[10:14], f.Stream)
	binary.BigEndian.PutUint32(b[14:18], f.Seq)
	binary.BigEndian.PutUint32(b[18:22], f.Ack)
	binary.BigEndian.PutUint64(b[22:30], f.Nonce)
	copy(b[30:], f.Data)
	h := hmac.New(sha256.New, []byte(key))
	h.Write(b[:len(b)-8])
	copy(b[len(b)-8:], h.Sum(nil)[:8])
	return b
}
func unpack(b []byte, key string) (frame, error) {
	if len(b) < headerSize+8 || len(b) > 4096 || b[0] != 1 {
		return frame{}, errors.New("invalid frame")
	}
	h := hmac.New(sha256.New, []byte(key))
	h.Write(b[:len(b)-8])
	if !hmac.Equal(h.Sum(nil)[:8], b[len(b)-8:]) {
		return frame{}, errors.New("authentication failed")
	}
	return frame{Op: b[1], Session: binary.BigEndian.Uint64(b[2:10]), Stream: binary.BigEndian.Uint32(b[10:14]), Seq: binary.BigEndian.Uint32(b[14:18]), Ack: binary.BigEndian.Uint32(b[18:22]), Nonce: binary.BigEndian.Uint64(b[22:30]), Data: append([]byte(nil), b[30:len(b)-8]...)}, nil
}
func query(f frame, p Profile) (*dns.Msg, error) {
	text := strings.ToLower(encoding.EncodeToString(f.pack(p.Key)))
	labels := []string{}
	for len(text) > 0 {
		n := min(63, len(text))
		labels = append(labels, text[:n])
		text = text[n:]
	}
	name := strings.Join(labels, ".") + "." + p.Domain + "."
	if len(name) > 254 {
		return nil, fmt.Errorf("request exceeds DNS name capacity")
	}
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeTXT)
	m.SetEdns0(1232, false)
	return m, nil
}
func extract(m *dns.Msg, domain, key string) (frame, error) {
	if len(m.Question) != 1 || m.Question[0].Qtype != dns.TypeTXT {
		return frame{}, errors.New("TXT required")
	}
	name := strings.ToLower(m.Question[0].Name)
	suffix := "." + strings.TrimSuffix(domain, ".") + "."
	if !strings.HasSuffix(name, suffix) {
		return frame{}, errors.New("wrong domain")
	}
	s := strings.ReplaceAll(strings.TrimSuffix(name, suffix), ".", "")
	b, e := encoding.DecodeString(strings.ToUpper(s))
	if e != nil {
		return frame{}, e
	}
	return unpack(b, key)
}
func response(req *dns.Msg, f frame, key string) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Compress = true
	m.Authoritative = true
	if opt := req.IsEdns0(); opt != nil {
		m.SetEdns0(min(opt.UDPSize(), 1232), false)
	}
	text := base64.RawStdEncoding.EncodeToString(f.pack(key))
	parts := []string{}
	for len(text) > 0 {
		n := min(240, len(text))
		parts = append(parts, text[:n])
		text = text[n:]
	}
	m.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0}, Txt: parts}}
	return m
}
func parseResponse(m *dns.Msg, key string) (frame, error) {
	if m.Truncated {
		return frame{}, errors.New("truncated DNS reply")
	}
	for _, rr := range m.Answer {
		if t, ok := rr.(*dns.TXT); ok {
			b, e := base64.RawStdEncoding.DecodeString(strings.Join(t.Txt, ""))
			if e != nil {
				return frame{}, e
			}
			return unpack(b, key)
		}
	}
	return frame{}, errors.New("missing tunnel TXT")
}
