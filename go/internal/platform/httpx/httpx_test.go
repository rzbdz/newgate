package httpx

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestUpstreamTransportKeepsProxyAndTimeout(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	t.Setenv("NO_PROXY", "")

	tr := UpstreamTransport(7 * time.Second)
	if tr.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v", tr.ResponseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Fatal("DialContext 未安装 MSS 兼容拨号器")
	}

	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}}
	got, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.String() != "http://127.0.0.1:3128" {
		t.Fatalf("上游代理 = %v", got)
	}
}

func TestUpstreamTransportBypassesProxyForLoopback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:3128")
	t.Setenv("NO_PROXY", "")

	tr := UpstreamTransport(0)
	req := &http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:8899"}}
	got, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("loopback 不应走代理，得到 %v", got)
	}
}
