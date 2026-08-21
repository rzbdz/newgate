package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rzbdz/newgate/go/internal/config"
	"github.com/rzbdz/newgate/go/internal/health"
	"github.com/rzbdz/newgate/go/internal/logx"
	"github.com/rzbdz/newgate/go/internal/paths"
	"github.com/rzbdz/newgate/go/internal/schemarepair"
)

type Server struct {
	Port   int
	Logger *log.Logger

	srv      *http.Server
	requests uint64
	failures uint64
	started  time.Time
}

func New(port int, lg *log.Logger) *Server {
	return &Server{Port: port, Logger: lg}
}

func (s *Server) logf(format string, a ...interface{}) {
	if s.Logger != nil {
		s.Logger.Printf(format, a...)
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/__newgate/status", s.handleStatus)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/", s.handleProxy)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		return fmt.Errorf("监听 127.0.0.1:%d 失败: %w", s.Port, err)
	}
	s.started = time.Now()
	s.srv = &http.Server{Handler: mux}
	s.logf("[proxy] 监听 127.0.0.1:%d", s.Port)
	return s.srv.Serve(ln)
}

func (s *Server) Shutdown() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := config.LoadState()
	writeJSON(w, 200, map[string]interface{}{
		"ok":       true,
		"profile":  st.ActiveProfile,
		"port":     s.Port,
		"requests": atomic.LoadUint64(&s.requests),
		"failures": atomic.LoadUint64(&s.failures),
		"uptime_s": int(time.Since(s.started).Seconds()),
		"roles":    config.Roles,
	})
}

// handleModels 让 opencode 能发现我们暴露的语义档位。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var data []map[string]interface{}
	for _, role := range config.Roles {
		data = append(data, map[string]interface{}{
			"id": role, "object": "model", "owned_by": "newgate",
		})
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

// route 一次解析的结果
type route struct {
	prov     config.Provider
	provName string
	model    string
}

// testChain 仅供测试注入，绕过磁盘配置与真实上游。生产路径永远是 nil。
// 有它才能保证单测不出网——见 docs/17 §1「测试不出网，出网即失败」。
var testChain func(role string) []config.Step

// resolveIn 在指定 profile 里解析档位。
func resolveIn(profileName, role string) (route, error) {
	pr, err := config.LoadProfile(profileName)
	if err != nil {
		return route{}, fmt.Errorf("profile %q 读不出: %v", profileName, err)
	}
	b, ok := pr.Resolve(role)
	if !ok {
		return route{}, errPassthrough{role}
	}
	provs, err := config.LoadProviders()
	if err != nil {
		return route{}, err
	}
	p, ok := provs.Providers[b.Provider]
	if !ok {
		return route{}, fmt.Errorf("provider %q 未定义", b.Provider)
	}
	if p.Key() == "" {
		return route{}, fmt.Errorf("provider %q 没有 api_key（设 %s 或写进 providers.json）", b.Provider, p.APIKeyEnv)
	}
	return route{prov: p, provName: b.Provider, model: b.Model}, nil
}

func roleOf(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:] // 剥掉 "newgate/" 前缀
	}
	return model
}

type errPassthrough struct{ role string }

func (e errPassthrough) Error() string { return "passthrough: " + e.role }

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	atomic.AddUint64(&s.requests, 1)

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.fail(w, 400, "读请求体失败: "+err.Error())
		return
	}
	_ = r.Body.Close()

	// 不做任何 JSON 往返。只在原始字节里读出 model，后面也只替换那一段。
	inModel, ok := TopLevelString(body, "model")
	if !ok || inModel == "" {
		s.fail(w, 400, "请求体顶层没有 model 字符串字段")
		return
	}
	role := roleOf(inModel)
	st := config.LoadState()
	stream0 := TopLevelBool(body, "stream")
	reqID := atomic.LoadUint64(&s.requests)
	// 进来就记——否则「请求没到」和「到了在等上游」在日志里长得一样
	s.logf("[proxy] #%d ← %s (档位 %s) stream=%v  开始", reqID, inModel, role, stream0)
	if st.DebugActive() {
		s.logf("[proxy] #%d 客户端请求 %s %s%s\n    body(%d字节): %s",
			reqID, r.Method, r.URL.Path, headerDump(r.Header), len(body),
			truncate(string(redact(body)), 4000))
	}

	// ---- 构造 fallback 链 ----
	// profile 为主序（链头优先，其余按 priority），list 为次序。
	// 语义：先在当前任务档位内部找替代，找不到才降到下一档。见 docs/18 §1。
	var steps []config.Step
	var skips []config.Skip
	if testChain != nil {
		steps = testChain(role)
		goto chained
	}
	{
		profiles, provs, _, cerr := config.LoadChainInputs()
		if cerr != nil {
			s.fail(w, 400, "配置读不出: "+cerr.Error()+"  ← 跑 `newgate doctor` 看详情")
			return
		}
		active := st.ActiveProfile
		// 兼容旧配置里的 fallback_profile：把它排到链头之后
		if st.FallbackProfile != "" {
			for _, p := range profiles {
				if p.Name == st.FallbackProfile && p.Prio() > 0 {
					lift := -1
					p.Priority = &lift
				}
			}
		}
		steps, skips = config.BuildChain(role, profiles, provs, config.ChainOpts{
			Active:    active,
			Available: health.Default.Available,
			MaxSteps:  st.Chain.Attempts(),
		})
	}
chained:
	if len(steps) == 0 {
		s.logf("[proxy] #%d 档位 %q 无可用候选。跳过原因：%s", reqID, role, fmtSkips(skips))
		s.fail(w, 404, fmt.Sprintf("档位 %q 没有可用候选（可用档位：%s）。跑 `newgate role %s` 看每个候选为什么被跳过",
			role, strings.Join(config.Roles, ", "), role))
		return
	}

	suffix := strings.TrimPrefix(r.URL.Path, "/v1")
	stream := stream0
	deadline := start.Add(time.Duration(st.Chain.Budget()) * time.Millisecond)

	var lastMsg string
	var lastCode int
	var trail []string // 给 X-Newgate-Chain
	for i, a := range steps {
		isLast := i == len(steps)-1
		if i > 0 && time.Now().After(deadline) {
			s.logf("[proxy] #%d 链总预算 %dms 用尽，停在第 %d 步",
				reqID, st.Chain.Budget(), i)
			trail = append(trail, "budget-exhausted")
			isLast = true
		}
		// 纯字节手术：只替换顶层 model 的值，其余每个字节原样保留
		newBody, merr := ReplaceTopLevelString(body, "model", a.Binding.Model)
		if merr != nil {
			s.fail(w, 500, "改写 model 失败: "+merr.Error())
			return
		}

		// tool schema 修补：只在真有东西要补时才重写 tools 这一个值，
		// messages / system / cache_control 仍然逐字节不动。
		if st.RepairEnabled() {
			if toolsRaw, ok := TopLevelRaw(newBody, "tools"); ok {
				repaired, changes, rerr := schemarepair.Repair(toolsRaw)
				switch {
				case rerr != nil:
					s.logf("[proxy] #%d tools 修补跳过（解析失败，按原样发）: %v", reqID, rerr)
				case len(changes) > 0:
					if nb, serr := ReplaceTopLevelRaw(newBody, "tools", repaired); serr == nil {
						newBody = nb
						s.logf("[proxy] #%d 补了 %d 个 tool 的 \"required\": []（语义无操作，"+
							"为通过严格校验器）: %v", reqID, len(changes), changes)
					} else {
						s.logf("[proxy] #%d tools 回写失败，按原样发: %v", reqID, serr)
					}
				}
			}
		}

		target := strings.TrimRight(a.Provider.BaseURL, "/") + suffix
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// 挂上客户端的 context：opencode 里按 ESC 取消时，上游请求也立刻中断，
		// 而不是让它跑完整个响应照样计费
		req, rerr := http.NewRequestWithContext(r.Context(), r.Method, target,
			bytes.NewReader(newBody))
		if rerr != nil {
			s.fail(w, 500, rerr.Error())
			return
		}
		s.dump(reqID, i, body, newBody)
		if st.DebugActive() {
			s.logf("[proxy] #%d 发往上游 %s\n    body(%d字节, 与原文差 %+d): %s",
				reqID, target, len(newBody), len(newBody)-len(body),
				truncate(string(redact(newBody)), 4000))
		}
		copyHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Del("Content-Length")
		setAuth(req.Header, a.Provider)

		// 流式不能设总超时（长响应会被砍断），但必须限制首字节等待时间，
		// 否则上游装死就永久挂住。ResponseHeaderTimeout 正好只管到响应头。
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = firstByteTimeout
		client := &http.Client{Transport: tr, Timeout: func() time.Duration {
			if stream {
				return 0
			}
			return totalTimeout
		}()}

		resp, derr := client.Do(req)
		routeStr := fmt.Sprintf("%s -> %s/%s", inModel, a.Binding.Provider, a.Binding.Model)

		if derr != nil {
			opened := health.Default.RecordFailure(a.Binding.Provider)
			atomic.AddUint64(&s.failures, 1)
			hint := ""
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				hint = fmt.Sprintf("  [首字节超过 %v——上游装死或排队]", firstByteTimeout)
			}
			s.logf("[proxy] #%d %s 连接失败: %v%s%s", reqID, routeStr, derr,
				breakerNote(opened, a.Binding.Provider), hint)
			lastMsg, lastCode = fmt.Sprintf("上游 %s 连接失败: %v", a.Binding.Provider, derr), 502
			trail = append(trail, fmt.Sprintf("%s(conn)", a.Binding))
			if !isLast {
				s.logf("[proxy] → 沿链下一步: %s", steps[i+1])
				continue
			}
			s.fail(w, 502, lastMsg)
			return
		}

		// 可转移的失败：还没往客户端写任何字节，安全
		if resp.StatusCode >= 400 && health.ShouldAdvance(resp.StatusCode, st.Chain.FallbackOn400) && !isLast {
			body, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			opened := health.Default.RecordFailure(a.Binding.Provider)
			atomic.AddUint64(&s.failures, 1)
			s.logf("[proxy] %s -> %d%s  上游说: %s", routeStr, resp.StatusCode,
				breakerNote(opened, a.Binding.Provider), trim(string(body)))
			s.logf("[proxy] → 沿链下一步: %s", steps[i+1])
			lastMsg, lastCode = trim(string(body)), resp.StatusCode
			trail = append(trail, fmt.Sprintf("%s(%d)", a.Binding, resp.StatusCode))
			continue
		}

		// 定案：把这个响应交给客户端
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			health.Default.RecordFailure(a.Binding.Provider)
			atomic.AddUint64(&s.failures, 1)

			// 错误响应体一般不大，整个读出来当证据，再原样转给客户端
			eb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 256*1024))
			base := s.saveErrEvidence(reqID, resp.StatusCode, body, newBody, eb,
				r.Header, resp.Header, routeStr)
			s.logf("[proxy] #%d 上游 %d，完整证据已存 %s.*", reqID, resp.StatusCode, base)
			s.logf("[proxy] #%d 上游原文: %s", reqID, truncate(string(redact(eb)), 2000))
			s.logf("[proxy] #%d 我们发出的 body(%d字节): %s", reqID, len(newBody),
				truncate(string(redact(newBody)), 4000))

			for k, vs := range resp.Header {
				if respHopHeaders[http.CanonicalHeaderKey(k)] || http.CanonicalHeaderKey(k) == "Content-Length" {
					continue
				}
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Newgate-Route", routeStr)
			w.Header().Set("X-Newgate-Profile", a.Profile)
			w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
			w.Header().Set("X-Newgate-Evidence", filepath.Base(base))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(eb)
			return
		}
		health.Default.RecordSuccess(a.Binding.Provider)
		if st.DebugActive() {
			s.logf("[proxy] #%d 上游响应头 %d%s", reqID, resp.StatusCode, headerDump(resp.Header))
		}
		s.logf("[proxy] #%d %s  %s  %d  首字节%dms  stream=%v  profile=%s%s",
			reqID, routeStr, suffix, resp.StatusCode, time.Since(start).Milliseconds(),
			stream, a.Profile, map[bool]string{true: "  (已转移)"}[i > 0])

		for k, vs := range resp.Header {
			if respHopHeaders[http.CanonicalHeaderKey(k)] {
				continue // Transfer-Encoding / Connection 由 Go 的 server 自己管
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("X-Newgate-Route", routeStr)
		w.Header().Set("X-Newgate-Profile", a.Profile)
		w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
		if i > 0 {
			// 转移了必须明确告知，绝不静默（docs/13 P0-5）
			// HTTP header 只能装 latin-1，中文会乱码——只放 ASCII，详情在日志里
			w.Header().Set("X-Newgate-Failover", fmt.Sprintf("%s -> %s (upstream %d; see: newgate logs)",
				steps[0].Profile, a.Profile, lastCode))
		}
		w.WriteHeader(resp.StatusCode)

		// 逐块转发。响应体一个字节都不改，也绝不缓冲整个响应。
		flusher, canFlush := w.(http.Flusher)
		buf := make([]byte, 32*1024)
		var chunks int
		var bytesOut int64
		for {
			n, rderr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					s.logf("[proxy] #%d 客户端断开（已转发 %d 块 / %d 字节）: %v",
						reqID, chunks, bytesOut, werr)
					return
				}
				chunks++
				bytesOut += int64(n)
				if canFlush {
					flusher.Flush() // 每读到就吐，不等缓冲区满
				}
			}
			if rderr != nil {
				switch {
				case rderr == io.EOF:
					if stream {
						s.logf("[proxy] #%d 流正常结束：%d 块 / %d 字节 / 总 %dms",
							reqID, chunks, bytesOut, time.Since(start).Milliseconds())
					}
				case r.Context().Err() != nil:
					s.logf("[proxy] #%d 客户端取消，已掐断上游（省下后续 token）", reqID)
				default:
					s.logf("[proxy] #%d 上游断流（已转发 %d 块 / %d 字节）: %v",
						reqID, chunks, bytesOut, rderr)
				}
				return
			}
		}
	}
}

// dump 在 NEWGATE_DUMP=1 时把「收到的」和「发出的」请求体落盘。
// 排查「是不是代理改坏了请求」时，这是唯一能拿出证据的手段。
func (s *Server) dump(reqID uint64, attempt int, in, out []byte) {
	if os.Getenv("NEWGATE_DUMP") == "" {
		return
	}
	dir := filepath.Join(paths.Config(), "dump")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	base := filepath.Join(dir, fmt.Sprintf("req-%06d-%d", reqID, attempt))
	_ = ioutil.WriteFile(base+".in.json", in, 0o600)
	_ = ioutil.WriteFile(base+".out.json", out, 0o600)
	same := "改写后与原文长度差 " + fmt.Sprint(len(out)-len(in)) + " 字节"
	s.logf("[proxy] #%d dump → %s.{in,out}.json  (%s)", reqID, base, same)
}

// redact 把请求/响应里像密钥的东西抹掉，日志和 dump 都用它。
var secretPat = regexp.MustCompile(`(sk-[A-Za-z0-9_\-]{8,}|Bearer\s+[A-Za-z0-9_\-\.]{8,})`)

func redact(b []byte) []byte {
	return secretPat.ReplaceAll(b, []byte("[REDACTED]"))
}

func headerDump(h http.Header) string {
	var keys []string
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		v := strings.Join(h[k], ", ")
		if k == "Authorization" || k == "X-Api-Key" || k == "Api-Key" {
			v = "[REDACTED]"
		}
		sb.WriteString("\n      " + k + ": " + v)
	}
	return sb.String()
}

// saveErrEvidence 上游报错时把完整证据落盘。这是排查
// 「是不是代理改坏了请求」唯一能拿出手的东西，所以不设开关。
func (s *Server) saveErrEvidence(reqID uint64, status int, inBody, outBody, respBody []byte,
	reqHdr http.Header, respHdr http.Header, routeStr string) string {
	dir := filepath.Join(paths.Config(), "dump")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	base := filepath.Join(dir, fmt.Sprintf("err-%03d-req%06d", status, reqID))
	_ = ioutil.WriteFile(base+".client-sent.json", redact(inBody), 0o600)
	_ = ioutil.WriteFile(base+".we-sent.json", redact(outBody), 0o600)
	_ = ioutil.WriteFile(base+".upstream-said.json", redact(respBody), 0o600)
	meta := fmt.Sprintf("route: %s\nstatus: %d\n\n--- 客户端请求头 ---%s\n\n--- 上游响应头 ---%s\n",
		routeStr, status, headerDump(reqHdr), headerDump(respHdr))
	_ = ioutil.WriteFile(base+".meta.txt", []byte(meta), 0o600)
	logx.PruneDir(dir, 20) // 只留最近 20 组证据，别把磁盘吃满
	return base
}

// chainHeader 描述链实际走了哪几步。ASCII only（HTTP header 装不了中文）。
func chainHeader(trail []string, final config.Step) string {
	all := append(append([]string{}, trail...), final.Binding.String()+"(ok)")
	return strings.Join(all, " -> ")
}

func fmtSkips(skips []config.Skip) string {
	var parts []string
	for _, sk := range skips {
		t := sk.Target
		if t == "" {
			t = sk.Profile
		}
		parts = append(parts, t+"="+sk.Reason)
	}
	return strings.Join(parts, "; ")
}

func breakerNote(opened bool, prov string) string {
	if opened {
		return fmt.Sprintf("  [熔断器已打开: %s 暂时摘掉]", prov)
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("…(截断，共 %d 字符)", len(r))
}

func trim(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 180 {
		return s[:180] + "…"
	}
	return s
}

func (s *Server) fail(w http.ResponseWriter, code int, msg string) {
	atomic.AddUint64(&s.failures, 1)
	s.logf("[proxy] 错误 %d: %s", code, msg)
	// 错误体里必须带 newgate 字样和排查命令（docs/16 §6.1）
	writeJSON(w, code, map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "newgate_error",
			"message": "[newgate] " + msg,
			"hint":    "排查：newgate status / newgate doctor / newgate stop（一键恢复直连）",
		},
	})
}

// firstByteTimeout 等上游第一个响应头的上限。
// 定 150s 是因为实测某些通道（anthropic-relay）有固定 ~43s 开销，
// 设太短会把本来能成功的请求误杀。
const firstByteTimeout = 150 * time.Second
const totalTimeout = 15 * time.Minute

// respHopHeaders 响应里必须剥掉的 hop-by-hop 头。
var respHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Te": true, "Trailer": true, "Upgrade": true, "Proxy-Authenticate": true,
}

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
	"Authorization": true, "X-Api-Key": true, // 客户端的假 key 一律丢掉
	"Content-Length": true, "Host": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func setAuth(h http.Header, p config.Provider) {
	key := p.Key()
	switch p.Protocol {
	case "anthropic":
		h.Set("x-api-key", key)
		if h.Get("anthropic-version") == "" {
			h.Set("anthropic-version", "2023-06-01")
		}
	default:
		h.Set("Authorization", "Bearer "+key)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.MarshalIndent(v, "", "  ")
	_, _ = w.Write(append(b, '\n'))
}
