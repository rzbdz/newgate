package probe

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestTLSErrorDetailExplainsRouteLoopOnTimeout(t *testing.T) {
	err := &net.DNSError{Err: "timeout", Name: "api.example.com", IsTimeout: true}
	got := tlsErrorDetail(err)
	for _, want := range []string{"timeout", "MTU/MSS", "Tailscale subnet route"} {
		if !strings.Contains(got, want) {
			t.Fatalf("诊断 %q 不包含 %q", got, want)
		}
	}
}

func TestTLSErrorDetailKeepsOriginalError(t *testing.T) {
	got := tlsErrorDetail(errors.New("x509: certificate expired"))
	if !strings.Contains(got, "x509: certificate expired") {
		t.Fatalf("原始错误被截断: %q", got)
	}
}
