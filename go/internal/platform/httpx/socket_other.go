//go:build !linux && !darwin

package httpx

import "syscall"

func setTCPMaxSegment(network, address string, c syscall.RawConn) error {
	return nil
}
