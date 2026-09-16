package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/modules/gateway/dialect"
	"github.com/rzbdz/newgate/go/modules/gateway/quirk"
)

// Result 同时保存主探活结论和附加方言能力；
// Latency 与 Total 分开，避免附加检查污染路由使用的首要健康评分。
type Result struct {
	Profile  string        `json:"profile"`
	Role     string        `json:"role"`
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	OK       bool          `json:"ok"`
	Status   int           `json:"status"`
	Latency  time.Duration `json:"latency_ms"`       // 主探活耗时，健康评分只用它
	Total    time.Duration `json:"total_latency_ms"` // 含方言/quirk 附加探测
	Err      string        `json:"error,omitempty"`
	Cached   bool          `json:"-"` // 同一个 provider/model 只真打一次
	// Dialects 探明支持的方言（"openai+anthropic"），空 = 主探活没过、没探。
	Dialects string `json:"dialects,omitempty"`
	// CountTokens anthropic 私有端点 /messages/count_tokens 是否可用。
	// nil = 没探到（主探活失败或连接错误）。
	CountTokens *bool `json:"count_tokens,omitempty"`
}

// Light 把探活结论压缩为终端状态灯，不参与实际路由决策。
func (r Result) Light() string {
	switch {
	case r.OK && r.Latency < 3*time.Second:
		return "🟢"
	case r.OK:
		return "🟡" // 通但慢
	default:
		return "🔴"
	}
}

// Target 是去重探活的最小身份；profile/role 只是引用它的配置视图。
type Target struct {
	Provider string
	Model    string
}

// String 返回稳定的 provider/model 标识，用于排序和诊断。
func (t Target) String() string { return t.Provider + "/" + t.Model }

// Options 控制一次探测。回调让调用方能实时渲染进展。
type Options struct {
	Only        string
	Timeout     time.Duration
	SlowAfter   time.Duration
	Concurrency int

	// OnPlan 在开始探测前调用一次，告知总共要打几个目标。
	OnPlan func(targets []Target)
	// OnDone 每个目标一完成就立刻调用（完成顺序，非固定顺序）。
	OnDone func(t Target, status int, lat, totalLat time.Duration, err error)
	// OnWaiting 定期告知还卡在哪些目标上，以及各自已等了多久。
	OnWaiting func(inflight map[Target]time.Duration)
	// WaitTick OnWaiting 的间隔，0 表示不启用。
	WaitTick time.Duration
}

// Run 把指定 profile（空 = 全部）的每个档位都打一遍。
// 同一个 (provider, model) 只真打一次，结果复用——省时间也省额度。
// 也会附带打一发 CheckQuirks 探上游毛病。
func Run(o Options) ([]Result, error) {
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.Timeout <= 0 {
		o.Timeout = 120 * time.Second
	}
	cache := loadCapabilityCache()

	provs, err := store.LoadProviders()
	if err != nil {
		return nil, err
	}
	names, err := store.ListProfiles()
	if err != nil {
		return nil, err
	}
	if o.Only != "" {
		found := false
		for _, n := range names {
			if n == o.Only {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("profile %q 不存在（可用：%s）", o.Only, strings.Join(names, ", "))
		}
		names = []string{o.Only}
	}

	// 收集去重后的探测目标
	var results []Result
	uniqSet := map[Target]bool{}
	var uniq []Target
	for _, n := range names {
		pr, err := store.LoadProfile(n)
		if err != nil {
			continue
		}
		for _, role := range domain.Roles {
			b, ok := pr.Resolve(role)
			if !ok {
				results = append(results, Result{Profile: n, Role: role, Err: "未绑定"})
				continue
			}
			r := Result{Profile: n, Role: role, Provider: b.Provider, Model: b.Model}
			if _, ok := provs.Providers[b.Provider]; !ok {
				r.Err = "provider 未定义"
				results = append(results, r)
				continue
			}
			t := Target{b.Provider, b.Model}
			if !uniqSet[t] {
				uniqSet[t] = true
				uniq = append(uniq, t)
			}
			results = append(results, r)
		}
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i].String() < uniq[j].String() })
	if o.OnPlan != nil {
		o.OnPlan(uniq)
	}

	type outcome struct {
		status   int
		lat      time.Duration
		totalLat time.Duration
		err      error
	}
	var mu sync.Mutex
	got := map[Target]outcome{}
	inflight := map[Target]time.Time{}

	// 定期汇报还卡在谁身上——claude 家族动辄 40s+，没这个用户会以为死了
	stopTick := make(chan struct{})
	if o.OnWaiting != nil && o.WaitTick > 0 {
		go func() {
			tk := time.NewTicker(o.WaitTick)
			defer tk.Stop()
			for {
				select {
				case <-stopTick:
					return
				case <-tk.C:
					mu.Lock()
					snap := map[Target]time.Duration{}
					for t, s := range inflight {
						snap[t] = time.Since(s)
					}
					mu.Unlock()
					if len(snap) > 0 {
						o.OnWaiting(snap)
					}
				}
			}
		}()
	}

	sem := make(chan struct{}, o.Concurrency)
	var wg sync.WaitGroup
	for _, t := range uniq {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			inflight[t] = time.Now()
			mu.Unlock()

			p := provs.Providers[t.Provider]
			totalStarted := time.Now()
			st, lat, err := timeoutRetry(func() (int, time.Duration, error) {
				return One(p, t.Model, o.Timeout)
			})
			if err == nil && st == http.StatusOK && o.SlowAfter > 0 && lat > o.SlowAfter {
				err = fmt.Errorf("极小请求耗时 %s，超过交互阈值 %s",
					lat.Round(time.Millisecond), o.SlowAfter)
			}

			if err == nil && st < 400 {
				// 方言/quirk 已学过就直接恢复缓存，不再重复花 token。
				if !cache.apply(t) {
					_ = CheckQuirks(t.Provider, p, t.Model, o.Timeout)
					CheckDialects(t.Provider, p, t.Model, o.Timeout)
					cache.capture(t)
				}
			}
			totalLat := time.Since(totalStarted)

			mu.Lock()
			delete(inflight, t)
			got[t] = outcome{st, lat, totalLat, err}
			mu.Unlock()

			if o.OnDone != nil {
				o.OnDone(t, st, lat, totalLat, err)
			}
		}(t)
	}
	wg.Wait()
	close(stopTick)
	_ = cache.save()

	// 回填
	seen := map[Target]bool{}
	for i := range results {
		r := &results[i]
		if r.Provider == "" || r.Err != "" {
			continue
		}
		t := Target{r.Provider, r.Model}
		o2 := got[t]
		r.Status, r.Latency, r.Total = o2.status, o2.lat, o2.totalLat
		r.OK = o2.err == nil && o2.status == 200
		if o2.err != nil {
			r.Err = o2.err.Error()
		}
		if seen[t] {
			r.Cached = true
		}
		seen[t] = true
		// 方言能力：注册表里探明的（同一个 provider/model 只探一次，行间共享）
		if ok, _ := dialect.Supports(r.Provider, r.Model, dialect.CapOpenAI); ok {
			r.Dialects = "openai"
		}
		if ok, _ := dialect.Supports(r.Provider, r.Model, dialect.CapAnthropic); ok {
			if r.Dialects != "" {
				r.Dialects += "+"
			}
			r.Dialects += "anthropic"
		}
		if ok, known := dialect.Supports(r.Provider, r.Model, dialect.CapCountTokens); known {
			ct := ok
			r.CountTokens = &ct
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Profile != results[j].Profile {
			return results[i].Profile < results[j].Profile
		}
		return roleIdx(results[i].Role) < roleIdx(results[j].Role)
	})
	return results, nil
}

func timeoutRetry(attempt func() (int, time.Duration, error)) (int, time.Duration, error) {
	status, latency, err := attempt()
	if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
		return status, latency, err
	}
	// 主延迟表示最终这次探测请求本身；第一次超时属于命令总耗时，
	// 不能叠到成功重试上把健康评分凭空翻倍。
	return attempt()
}

// Light 给一次探测结果配灯。
func Light(ok bool, lat time.Duration) string {
	switch {
	case ok && lat < 3*time.Second:
		return "🟢"
	case ok:
		return "🟡"
	}
	return "🔴"
}

// One 对一个 (provider, model) 打一次最小请求。
func One(p domain.Provider, model string, timeout time.Duration) (int, time.Duration, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":      model,
		"max_tokens": 4,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	suffix := "/chat/completions"
	if p.Protocol == "anthropic" {
		suffix = "/messages"
	}
	// 按方言挑 base：两种方言分家的上游（provider.anthropic_url）只有走对
	// base 才通，探错 base 会得到一个和真实流量无关的结论。
	url := p.URL(suffix)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Protocol == "anthropic" {
		req.Header.Set("x-api-key", p.Key())
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Key())
	}

	start := time.Now()
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	lat := time.Since(start)
	if err != nil {
		return 0, lat, err
	}
	defer resp.Body.Close()
	raw, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, lat, fmt.Errorf("%s", extractErr(raw))
	}
	return resp.StatusCode, lat, nil
}

func extractErr(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func roleIdx(r string) int {
	for i, x := range domain.Roles {
		if x == r {
			return i
		}
	}
	return 99
}

// Summary 按 profile 汇总，用于给出「该切哪个」的建议。
type Summary struct {
	Profile string
	OK      int
	Bad     int
	AvgMs   int64
	Grade   string
}

func Summarize(rs []Result) []Summary {
	m := map[string]*Summary{}
	seen := map[string]bool{}
	var order []string
	for _, r := range rs {
		s, ok := m[r.Profile]
		if !ok {
			s = &Summary{Profile: r.Profile}
			m[r.Profile] = s
			order = append(order, r.Profile)
		}
		target := r.Profile + "\x00" + r.Provider + "\x00" + r.Model
		if seen[target] {
			continue
		}
		seen[target] = true
		if r.OK {
			s.OK++
			s.AvgMs += r.Latency.Milliseconds()
			if r.Latency >= 3*time.Second {
				s.Grade = "usable"
			} else if s.Grade == "" {
				s.Grade = "fluent"
			}
		} else {
			s.Bad++
			s.Grade = "unavailable"
		}
	}
	var out []Summary
	for _, n := range order {
		s := m[n]
		if s.OK > 0 {
			s.AvgMs /= int64(s.OK)
		}
		out = append(out, *s)
	}
	return out
}