package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/rzbdz/newgate/go/modules/gateway/controlpath"
	"net/http"
	"time"

	"github.com/rzbdz/newgate/go/lib/httpx"
	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// 代理自报的运行时状态。整页状态（status / metrics / st）都只发**一次**
// 这个请求就拿全——以前每个命令各打各的探活，数字还可能对不上。
type proxyInfo struct {
	OK       bool                `json:"ok"`
	Port     int                 `json:"port"`
	UptimeS  int                 `json:"uptime_s"`
	Requests uint64              `json:"requests"`
	Failures uint64              `json:"failures"`
	Breakers []breakerapi.Status `json:"breakers"`
	Handoff  bool                `json:"handoff"`
	Default  string              `json:"default_profile"`
	Active   map[string]string   `json:"active"`
	Think    struct {
		Entries   int   `json:"entries"`
		Bytes     int64 `json:"bytes"`
		MaxBytes  int64 `json:"max_bytes"`
		Hits      int64 `json:"hits"`
		Misses    int64 `json:"misses"`
		Evictions int64 `json:"evictions"`
	} `json:"thinkcache"`
}

// availableFromProxy 把 daemon 的全局熔断表冻结成一次 CLI 命令内的一致快照。
// daemon 不在线时不凭空判坏：诊断退化为只看静态配置。
func availableFromProxy(ps *proxyInfo) func(provider, model string) bool {
	blocked := map[string]bool{}
	if ps != nil {
		for _, b := range ps.Breakers {
			if b.Open {
				blocked[b.Provider+"/"+b.Model] = true
			}
		}
	}
	return func(provider, model string) bool {
		return !blocked[provider+"/"+model]
	}
}

// rankFromProxy 把 daemon 算好的排序键原样递给诊断。
//
// **这里不重算阈值**：3000ms / 12000ms 那套分桶只存在于 modules/breaker，
// CLI 抄一份的话 daemon 改阈值 CLI 不会跟着变（2026-09-17 之前就是这样，
// 两边各有一份 3000/12000）。排序策略只有一个来源。
//
// 老 daemon 不发 `rank`（优雅交接期间 CLI 与 daemon 可以来自不同版本），读不到
// 就退化成中性值——排序退化为「按配置顺序」，不会因为版本不齐而互相打架。
// 被摘牌的 binding 不在这里沉底：建链期先问 Available，被摘的根本进不了候选。
func rankFromProxy(ps *proxyInfo) func(provider, model string) int {
	const neutral = 1_000_000
	scores := map[string]int{}
	if ps != nil {
		for _, b := range ps.Breakers {
			score := neutral
			if b.Rank != 0 {
				score = b.Rank
			}
			scores[b.Provider+"/"+b.Model] = score
		}
	}
	return func(provider, model string) int {
		if score, ok := scores[provider+"/"+model]; ok {
			return score
		}
		return neutral
	}
}

func healthFromProxy(ps *proxyInfo) map[string]breakerapi.Status {
	out := map[string]breakerapi.Status{}
	if ps != nil {
		for _, status := range ps.Breakers {
			out[status.Provider+"/"+status.Model] = status
		}
	}
	return out
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
	if !localGet(info.Port, controlpath.Status, &s) {
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
	if !localGet(port, controlpath.Metrics, &out) {
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

func localPost(port int, path, token string, body, out interface{}) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpx.LocalClient(3 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon 返回 HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
