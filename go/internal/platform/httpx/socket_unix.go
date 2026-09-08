//go:build linux || darwin

package httpx

import (
	"syscall"
)

func setTCPMaxSegment(network, address string, c syscall.RawConn) error {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil
	}
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(
			int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, UpstreamTCPMaxSegment)
	}); err != nil {
		return err
	}
	return sockErr
}
