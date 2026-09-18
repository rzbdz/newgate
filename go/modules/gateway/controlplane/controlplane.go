// Package controlplane 是与**跑着的守护进程**控制面打交道的客户端，以及那份
// 状态文档的形状。
//
// # 为什么它是一个共享叶子（2026-09-18）
//
// 这套东西原来长在 modules/cli 里（proxy.go + lifecycle.go 的一小块）。后果是
// 只有界面读得到守护进程的自报状态，于是**每个想读它的模块都被钉住了**：
// `newgate metrics`（计数器的语义归 gateway）、`newgate breaker`（健康表归
// breaker）、`newgate probe`（探活结果要回传给守护进程记账）——它们的知识明明
// 各自归自己，命令却只能写在界面里，因为界面是唯一持有那个 HTTP 客户端的地方。
//
// 抽成共享叶子之后，谁都能读守护进程了，那些命令也就搬得回自己的模块。
//
// # 它为什么不是一个模块
//
// 它没有状态、没有生命周期、不需要被启动或停止——就是一组对 loopback 控制面的
// 读写函数（外加一个发 SIGHUP 的便捷口）。这跟 lib/ 里那些纯工具同类，只是它
// 认识 newgate 的控制面协议，所以留在 gateway 这一侧（控制面的路径与文档都由
// 数据面定义，见 controlpath）。
//
// # 依赖方向
//
// controlplane → controlpath（路径）、breaker/status（文档里的 wire 类型）、
// runtime/daemon（pidfile）、lib/httpx。四个都是叶子或工具，没有回边，所以
// breaker 自己、gateway 自己、界面都能引它。
package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/go/lib/httpx"
	"github.com/rzbdz/newgate/go/modules/breaker/status"
	"github.com/rzbdz/newgate/go/modules/gateway/controlpath"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// Think 是推理内容缓存的计数。**只报计数，永远不报内容。**
type Think struct {
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
	MaxBytes  int64 `json:"max_bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
}

// Doc 是守护进程自报的运行时状态（`/__newgate/status` 的文档）。
//
// 整页状态（status / metrics / st）都只发**一次**这个请求就拿全——以前每个命令
// 各打各的探活，数字还可能对不上。
type Doc struct {
	OK       bool              `json:"ok"`
	Port     int               `json:"port"`
	UptimeS  int               `json:"uptime_s"`
	Requests uint64            `json:"requests"`
	Failures uint64            `json:"failures"`
	Breakers []status.Status   `json:"breakers"`
	Handoff  bool              `json:"handoff"`
	Default  string            `json:"default_profile"`
	Active   map[string]string `json:"active"`
	Think    Think             `json:"thinkcache"`
}

// Info 是守护进程的进程信息（来自 pidfile）。
type Info = daemon.Info

// State 取守护进程的进程信息 + 自报状态。
//
// pid 来自 pidfile（进程还在不在），doc 来自 HTTP（它自己怎么想的）。两者都可能
// 单独失败：进程活着但端口不响应，正是「出站代理劫持 loopback」那个经典故障，
// 必须能分开表达（见 newgate doctor）。
func State() (info *Info, doc *Doc) {
	info = daemon.Running()
	if info == nil || info.Port <= 0 {
		return info, nil
	}
	var d Doc
	if !get(info.Port, controlpath.Status, &d) {
		return info, nil
	}
	return info, &d
}

// Metrics 网关计数器与 uptime。和 status 是两个端点（计数器是守护进程内存里的
// 一张 map，status 只挑几个数报），所以单独取一次。
func Metrics(port int) (map[string]uint64, int, bool) {
	var out struct {
		Metrics map[string]uint64 `json:"metrics"`
		UptimeS int               `json:"uptime_s"`
	}
	if !get(port, controlpath.Metrics, &out) {
		return nil, 0, false
	}
	return out.Metrics, out.UptimeS, true
}

// Ping 端口上的控制面活着吗（HTTP 优先，退化成 TCP 探活）。
func Ping(port int) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Status))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond)
}

// Post 往控制面发一个带令牌的 POST。
func Post(port int, path, token string, body, out interface{}) error {
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
		return fmt.Errorf("守护进程返回 HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Notify 让运行中的守护进程立刻重读配置。
//
// 平时配置改动靠 watcher 的 1 秒轮询，对**手工编辑文件**足够快；但命令改配置是
// 用户刚敲下的，必须立刻生效，所以显式发 SIGHUP 强制重载。
func Notify() {
	if i := daemon.Running(); i != nil {
		_ = syscall.Kill(i.PID, syscall.SIGHUP)
	}
}

func get(port int, path string, v interface{}) bool {
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
