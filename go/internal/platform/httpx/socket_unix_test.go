//go:build linux || darwin

package httpx

import (
	"net"
	"syscall"
	"testing"
	"time"
)

func TestDialUpstreamCapsTCPMaxSegment(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	conn, err := DialUpstreamTimeout("tcp4", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer := <-accepted
	defer peer.Close()

	tc, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatalf("连接类型 = %T", conn)
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	if err := raw.Control(func(fd uintptr) {
		got, err = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG)
	}); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got <= 0 || got > UpstreamTCPMaxSegment {
		t.Fatalf("TCP_MAXSEG = %d，期望不超过 %d", got, UpstreamTCPMaxSegment)
	}
}
