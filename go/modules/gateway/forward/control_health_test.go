package forward

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/go/modules/breaker"
)

func TestProbeHealthOpensOnlySlowBinding(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	_ = ioutil.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o600)
	const tok = "ctrl-tok-0123456789abcdef0123456789abcdef"
	_ = ioutil.WriteFile(filepath.Join(dir, "state.json"), []byte(
		`{"default_profile":"ds","port":1,"control_token":"`+tok+
			`","timeouts":{"classifier_first_byte_ms":10}}`), 0o600)

	const provider = "relay-health-test"
	srv := New(0, nil, nil, breaker.NewTable())
	front := httptest.NewServer(http.HandlerFunc(srv.handleHealth))
	defer front.Close()
	body := []byte(`{"observations":[` +
		`{"provider":"` + provider + `","model":"slow","status":200,"latency_ms":20,"context_bytes":1},` +
		`{"provider":"` + provider + `","model":"fast","status":200,"latency_ms":5,"context_bytes":1}` +
		`]}`)
	req, _ := http.NewRequest(http.MethodPost, front.URL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health POST = %d", resp.StatusCode)
	}
	if srv.Health.Available(provider, "slow") {
		t.Fatal("超过 classifier 阈值的极小请求没有熔断")
	}
	if !srv.Health.Available(provider, "fast") {
		t.Fatal("同 provider 的快速模型被误伤")
	}
}
