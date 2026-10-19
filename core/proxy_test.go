package core

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestHTTPThroughSOCKS(t *testing.T) {
	p, _ := testProfile(t)
	c, _ := NewClient(p, nil)
	defer c.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go c.ServeSOCKS(listener)
	payload := bytes.Repeat([]byte("HTTP over racing DNS\n"), 2048)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer target.Close()
	dialer, e := proxy.SOCKS5("tcp", listener.Addr().String(), nil, proxy.Direct)
	if e != nil {
		t.Fatal(e)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	resp, e := httpClient.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	out, e := io.ReadAll(resp.Body)
	if e != nil || !bytes.Equal(out, payload) {
		t.Fatalf("HTTP transfer failed: %v (%d bytes)", e, len(out))
	}
}
func TestSOCKSUDPAssociate(t *testing.T) {
	p, _ := testProfile(t)
	c, _ := NewClient(p, nil)
	defer c.Close()
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	defer listener.Close()
	go c.ServeSOCKS(listener)
	echo, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		b := make([]byte, 2048)
		n, a, e := echo.ReadFrom(b)
		if e == nil {
			echo.WriteTo(b[:n], a)
		}
	}()
	control, e := net.Dial("tcp", listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(10 * time.Second))
	control.Write([]byte{5, 1, 0})
	var auth [2]byte
	if _, e = io.ReadFull(control, auth[:]); e != nil || auth[1] != 0 {
		t.Fatal("SOCKS auth", e)
	}
	control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	var header [4]byte
	if _, e = io.ReadFull(control, header[:]); e != nil || header[1] != 0 {
		t.Fatal("SOCKS UDP", e)
	}
	address, e := readAddress(control, header[3])
	if e != nil {
		t.Fatal(e)
	}
	udp, e := net.Dial("udp", address)
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	udp.SetDeadline(time.Now().Add(10 * time.Second))
	packet, _ := appendAddress([]byte{0, 0, 0}, echo.LocalAddr().String())
	packet = append(packet, []byte("datagram via SOCKS")...)
	udp.Write(packet)
	b := make([]byte, 2048)
	n, e := udp.Read(b)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(b[:n], packet) {
		t.Fatal("SOCKS UDP reply mismatch")
	}
}
func TestSocketRebindPreservesStream(t *testing.T) {
	p, _ := testProfile(t)
	c, _ := NewClient(p, nil)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, e := c.DialContext(ctx, "tcp", echoTCP(t))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	for _, data := range []string{"before network change", "after network change"} {
		if _, e = conn.Write([]byte(data)); e != nil {
			t.Fatal(e)
		}
		out := make([]byte, len(data))
		if _, e = io.ReadFull(conn, out); e != nil || string(out) != data {
			t.Fatal("stream did not survive socket rebind", e)
		}
		c.ResetSockets()
	}
}
