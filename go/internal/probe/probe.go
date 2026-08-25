package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/gateway/quirk"
	"github.com/rzbdz/newgate/go/internal/store"
)

type Result struct {
	Profile  string        `json:"profile"`
	Role     string        `json:"role"`
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	OK       bool          `json:"ok"`
	Status   int           `json:"status"`
	Latency  time.Duration `json:"latency_ms"`
	Err      string        `json:"error,omitempty"`
	Cached   bool          `json:"-"` // 同一个 provider/model 只真打一次
}

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

type Target struct {
	Provider string
	Model    string
}

func (t Target) String() string { return t.Provider + "/" + t.Model }

// Options 控制一次探测。回调让调用方能实时渲染进展。
type Options struct {
	Only        string
	Timeout     time.Duration
	Concurrency int

	// OnPlan 在开始探测前调用一次，告知总共要打几个目标。
	OnPlan func(targets []Target)
	// OnDone 每个目标一完成就立刻调用（完成顺序，非固定顺序）。
	OnDone func(t Target, status int, lat time.Duration, err error, done, total int)
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
		status int
		lat    time.Duration
		err    error
	}
	var mu sync.Mutex
	got := map[Target]outcome{}
	inflight := map[Target]time.Time{}
	doneN := 0
	total := len(uniq)

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
			st, lat, err := One(p, t.Model, o.Timeout)

			if err == nil && st < 400 {
				// 普通探活通过了，顺手探一发怪癖（并发度够，多打一发不慢）
				_ = CheckQuirks(t.Provider, p, t.Model, o.Timeout)
			}

			mu.Lock()
			delete(inflight, t)
			got[t] = outcome{st, lat, err}
			doneN++
			n := doneN
			mu.Unlock()

			if o.OnDone != nil {
				o.OnDone(t, st, lat, err, n, total)
			}
		}(t)
	}
	wg.Wait()
	close(stopTick)

	// 回填
	seen := map[Target]bool{}
	for i := range results {
		r := &results[i]
		if r.Provider == "" || r.Err != "" {
			continue
		}
		t := Target{r.Provider, r.Model}
		o2 := got[t]
		r.Status, r.Latency = o2.status, o2.lat
		r.OK = o2.err == nil && o2.status == 200
		if o2.err != nil {
			r.Err = o2.err.Error()
		}
		if seen[t] {
			r.Cached = true
		}
		seen[t] = true
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Profile != results[j].Profile {
			return results[i].Profile < results[j].Profile
		}
		return roleIdx(results[i].Role) < roleIdx(results[j].Role)
	})
	return results, nil
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
	url := strings.TrimRight(p.BaseURL, "/") + suffix

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
}

func Summarize(rs []Result) []Summary {
	m := map[string]*Summary{}
	var order []string
	for _, r := range rs {
		s, ok := m[r.Profile]
		if !ok {
			s = &Summary{Profile: r.Profile}
			m[r.Profile] = s
			order = append(order, r.Profile)
		}
		if r.OK {
			s.OK++
			s.AvgMs += r.Latency.Milliseconds()
		} else {
			s.Bad++
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

// CheckQuirks 主动探一发「带 tools 的请求」，把上游的毛病提前学出来。
//
// 为什么必须单独探这一发：普通探活发的是一句 "hi"，**不带 tools**。而
// glm-5.3 那个 400 恰恰只在带 tools 时出现——聚合器看见 tools 才会替我们
// 塞「关闭思考」。于是 `newgate probe` 一片全绿，真实流量却每条都 400，
// 探活比现实乐观是最坏的一种探活。
//
// 只在普通探活通过后再打，且一个 (provider, model) 只打一次：多打一发就多
// 一次额度和一次撞限流的机会。
//
// 返回学到的毛病（人话）。什么都没学到就返回 nil——包括请求本身失败的情况：
// 探测失败不代表模型有毛病，不能凭猜往注册表里写。
func CheckQuirks(provName string, p domain.Provider, model string, timeout time.Duration) []string {
	payload := map[string]interface{}{
		"model":      model,
		"max_tokens": 4,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"tools": []map[string]interface{}{{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "newgate_probe",
				"description": "probe",
				"parameters": map[string]interface{}{
					"type": "object", "properties": map[string]interface{}{}, "required": []string{},
				},
			},
		}},
	}
	suffix := "/chat/completions"
	if p.Protocol == "anthropic" {
		// Anthropic 方言的 tools 是平铺的，没有 function 包一层
		payload["tools"] = []map[string]interface{}{{
			"name": "newgate_probe", "description": "probe",
			"input_schema": map[string]interface{}{
				"type": "object", "properties": map[string]interface{}{}, "required": []string{},
			},
		}}
		suffix = "/messages"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}

	req, err := http.NewRequest("POST", strings.TrimRight(p.BaseURL, "/")+suffix,
		bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Protocol == "anthropic" {
		req.Header.Set("x-api-key", p.Key())
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Key())
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, _ := ioutil.ReadAll(resp.Body) // error body is small
	return quirk.Learn(provName, model, resp.StatusCode, raw)
}
