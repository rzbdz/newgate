package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rzbdz/newgate/go/internal/platform/httpx"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
)

// 代理自报的运行时状态。整页状态（status / metrics / st）都只发**一次**
// 这个请求就拿全——以前每个命令各打各的探活，数字还可能对不上。
type proxyInfo struct {
	OK       bool              `json:"ok"`
	Port     int               `json:"port"`
	UptimeS  int               `json:"uptime_s"`
	Requests uint64            `json:"requests"`
	Failures uint64            `json:"failures"`
	Handoff  bool              `json:"handoff"`
	Default  string            `json:"default_profile"`
	Active   map[string]string `json:"active"`
	Think    struct {
		Entries   int   `json:"entries"`
		Bytes     int64 `json:"bytes"`
		MaxBytes  int64 `json:"max_bytes"`
		Hits      int64 `json:"hits"`
		Misses    int64 `json:"misses"`
		Evictions int64 `json:"evictions"`
	} `json:"thinkcache"`
}

// proxyState 代理的进程信息 + 自报状态。
//
// pid 来自 pidfile（进程还在不在），info 来自 HTTP（它自己怎么想的）。
// 两者都可能单独失败：进程活着但端口不响应，正是「出站代理劫持 loopback」
// 那个经典故障，必须能分开表达（见 doctor）。
func proxyState() (info *daemon.Info, st *proxyInfo) {
	info = daemon.Running()
	if info == nil || info.Port <= 0 {
		return info, nil
	}
	var s proxyInfo
	if !localGet(info.Port, "/__newgate/status", &s) {
		return info, nil
	}
	return info, &s
}

// proxyMetrics 网关计数器。和 status 是两个端点（计数器是 daemon 内存里
// 的一张 map，status 只挑几个数报），所以单独取一次。
func proxyMetrics(port int) (map[string]uint64, int, bool) {
	var out struct {
		Metrics map[string]uint64 `json:"metrics"`
		UptimeS int               `json:"uptime_s"`
	}
	if !localGet(port, "/__newgate/metrics", &out) {
		return nil, 0, false
	}
	return out.Metrics, out.UptimeS, true
}

func localGet(port int, path string, v interface{}) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(v) == nil
}
