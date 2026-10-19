package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// ServeSOCKS serves a loopback SOCKS5 proxy, including RFC1928 UDP associations.
func (c *Client) ServeSOCKS(l net.Listener) error {
	go func() { <-c.ctx.Done(); l.Close() }()
	for {
		conn, e := l.Accept()
		if e != nil {
			return e
		}
		c.proxies.Store(conn, true)
		go c.handleSOCKS(conn)
	}
}
func readAddress(r io.Reader, atyp byte) (string, error) {
	var host string
	switch atyp {
	case 1:
		b := make([]byte, 4)
		if _, e := io.ReadFull(r, b); e != nil {
			return "", e
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, e := io.ReadFull(r, b); e != nil {
			return "", e
		}
		host = net.IP(b).String()
	case 3:
		var n [1]byte
		if _, e := io.ReadFull(r, n[:]); e != nil {
			return "", e
		}
		if n[0] == 0 {
			return "", fmt.Errorf("empty hostname")
		}
		b := make([]byte, int(n[0]))
		if _, e := io.ReadFull(r, b); e != nil {
			return "", e
		}
		host = string(b)
	default:
		return "", fmt.Errorf("unsupported address type")
	}
	var port [2]byte
	if _, e := io.ReadFull(r, port[:]); e != nil {
		return "", e
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}
func appendAddress(dst []byte, address string) ([]byte, error) {
	h, p, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ip := net.ParseIP(h)
	if v := ip.To4(); v != nil {
		dst = append(dst, 1)
		dst = append(dst, v...)
	} else if ip != nil {
		dst = append(dst, 4)
		dst = append(dst, ip.To16()...)
	} else {
		if len(h) > 255 {
			return nil, fmt.Errorf("hostname too long")
		}
		dst = append(dst, 3, byte(len(h)))
		dst = append(dst, h...)
	}
	n, e := strconv.Atoi(p)
	if e != nil {
		return nil, e
	}
	dst = append(dst, byte(n>>8), byte(n))
	return dst, nil
}
func (c *Client) handleSOCKS(conn net.Conn) {
	defer conn.Close()
	defer c.proxies.Delete(conn)
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	var h [2]byte
	if _, e := io.ReadFull(conn, h[:]); e != nil || h[0] != 5 || h[1] == 0 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, e := io.ReadFull(conn, methods); e != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
		}
	}
	if !noAuth {
		conn.Write([]byte{5, 255})
		return
	}
	if e := writeFull(conn, []byte{5, 0}); e != nil {
		return
	}
	var req [4]byte
	if _, e := io.ReadFull(conn, req[:]); e != nil || req[0] != 5 || req[2] != 0 {
		return
	}
	address, e := readAddress(conn, req[3])
	if e != nil {
		return
	}
	if req[1] == 3 {
		c.serveUDPAssociation(conn)
		return
	}
	if req[1] != 1 {
		conn.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	remote, e := c.DialContext(ctx, "tcp", address)
	cancel()
	if e != nil {
		conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()
	if e = writeFull(conn, []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	remote.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		io.Copy(remote, conn)
		if cw, ok := remote.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		close(done)
	}()
	io.Copy(conn, remote)
	conn.Close()
	remote.Close()
	<-done
}

type udpFlow struct {
	conn net.Conn
	last time.Time
}

func (c *Client) serveUDPAssociation(control net.Conn) {
	udp, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		return
	}
	defer udp.Close()
	reply, _ := appendAddress([]byte{5, 0, 0}, udp.LocalAddr().String())
	if e = writeFull(control, reply); e != nil {
		return
	}
	control.SetDeadline(time.Time{})
	go func() { io.Copy(io.Discard, control); udp.Close() }()
	var mu sync.Mutex
	flows := map[string]*udpFlow{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, f := range flows {
			f.conn.Close()
		}
	}()
	var source *net.UDPAddr
	for {
		b := make([]byte, 65535)
		udp.SetReadDeadline(time.Now().Add(2 * time.Minute))
		n, from, e := udp.ReadFromUDP(b)
		if e != nil {
			return
		}
		if source == nil {
			source = from
		}
		if from.String() != source.String() || n < 4 || b[0] != 0 || b[1] != 0 || b[2] != 0 {
			continue
		}
		r := &byteReader{b: b[4:n]}
		address, e := readAddress(r, b[3])
		if e != nil {
			continue
		}
		payload := append([]byte(nil), r.b...)
		mu.Lock()
		flow := flows[address]
		if flow == nil {
			if len(flows) >= 128 {
				mu.Unlock()
				continue
			}
			ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
			remote, err := c.DialContext(ctx, "udp", address)
			cancel()
			if err != nil {
				mu.Unlock()
				continue
			}
			flow = &udpFlow{conn: remote}
			flows[address] = flow
			dest := source
			go func(remote net.Conn, address string) {
				buf := make([]byte, 65535)
				for {
					remote.SetReadDeadline(time.Now().Add(90 * time.Second))
					n, e := remote.Read(buf)
					if e != nil {
						break
					}
					_, port, _ := net.SplitHostPort(address)
					if port == "53" {
						c.observeDNS(buf[:n])
					}
					out, _ := appendAddress([]byte{0, 0, 0}, address)
					out = append(out, buf[:n]...)
					udp.WriteToUDP(out, dest)
				}
				remote.Close()
				mu.Lock()
				if current := flows[address]; current != nil && current.conn == remote {
					delete(flows, address)
				}
				mu.Unlock()
			}(remote, address)
		}
		flow.last = time.Now()
		mu.Unlock()
		flow.conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
		if _, e = flow.conn.Write(payload); e != nil {
			flow.conn.Close()
		}
	}
}

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

type datagramConn struct {
	net.Conn
	readMu, writeMu sync.Mutex
}

func (d *datagramConn) Write(b []byte) (int, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if len(b) > 65507 {
		return 0, fmt.Errorf("UDP datagram too large")
	}
	p := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(p, uint32(len(b)))
	copy(p[4:], b)
	if e := writeFull(d.Conn, p); e != nil {
		return 0, e
	}
	return len(b), nil
}
func (d *datagramConn) Read(b []byte) (int, error) {
	d.readMu.Lock()
	defer d.readMu.Unlock()
	var h [4]byte
	if _, e := io.ReadFull(d.Conn, h[:]); e != nil {
		return 0, e
	}
	n := int(binary.BigEndian.Uint32(h[:]))
	if n > 65507 {
		return 0, fmt.Errorf("invalid datagram length")
	}
	data := make([]byte, n)
	if _, e := io.ReadFull(d.Conn, data); e != nil {
		return 0, e
	}
	return copy(b, data), nil
}
