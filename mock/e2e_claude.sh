#!/usr/bin/env bash
# 零 token 端到端：`newgate claude --profile=ds|glm` 的 ds↔glm 切换，
# 加上 Claude Code 形态的思考模式多轮对话。
#
# 验证（对应本次特性/修复）：
#   1. 启动时注入的 env 是**真实模型名**（claude 界面显示 deepseek-chat，
#      而不是 heavy），且 base URL 带上 /a/claude/p/<profile>。
#   2. 代理把真实模型名反解回档位，转发给正确的上游。
#   3. DeepSeek 思考模式：Claude Code 会把 thinking 块剥掉，代理必须补回——
#      优先 thinkcache 里那轮的真实原文（tool id 找回），查不到就补**非空**
#      占位符。假上游按官方最严口径校验（带 tools + 思考开 → assistant 必须
#      回传非空推理，空串照样 400），所以这里 200 = 修复真的生效。
#   4. count_tokens：Claude Code 周期性调用（水位条/自动压缩阈值）。上游
#      听得懂（原生 anthropic 端点）就转发拿真值、model 按 mid 链头补上；
#      听不懂的（聚合器 404）由 forward 层 lazy probe 学下来退回本地粗估
#      （单测覆盖）。
#   5. 控制端点 /__newgate/stop：多用户共享部署下，读得到配置却发不出
#      信号的用户靠它停机——错令牌 403，对令牌让 daemon 退干净。
#   6. 优雅交接 /__newgate/upgrade（nginx 式零停机升级）：restart 把监听
#      socket 移交给新进程，在途 SSE 流由旧进程流完为止——开发 newgate
#      的会话本身就穿行在代理里，这是「能持续开发」的前提。
#   7. 后台请求：分类器整条链改走 light、其余只禁思考。Claude Code 的非流
#      式后台调用不带 thinking，国模却把缺省当默认思考 → 15-30 秒、成波超
#      时。代理一律补 thinking:disabled（缺就补、带了也改写）；其中 Bash
#      安全分类器本体（实抓特征：system 开头 "You are a security monitor…"）
#      在**建链之前**改道 light 档——含 fallback，light 挂了沿 light 链换
#      人，不回 mid；其他后台调用（compact 总结这类）不改道；主循环的流式
#      请求不受影响（think1/2/3 正是流式，思考链路原样走）。
#   8. 窗口声明：Claude Code 不认识注入的真实模型名（glm-4-plus），按
#      「未知模型」默认 200k 窗口提前 compact。profile 里声明了
#      context_window/auto_compact_window 就在启动时注入对应的
#      CLAUDE_CODE_* env；没声明的 profile 一个都不注入。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config / ~/.claude / shell rc。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/go/bin/newgate"
SANDBOX="$(mktemp -d /tmp/newgate-claude-e2e.XXXXXX)"
UP_PORT=18081
PROXY_PORT=18898
FAKEBIN="$SANDBOX/fakebin"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
mkdir -p "$NEWGATE_HOME/mappings" "$FAKEBIN"
# 沙箱要密闭：跑 e2e 的会话自己可能带着 newgate 注入的窗口声明（嵌套
# 启动时父进程 env 会漏给子进程），不 unset 会让「没声明的 profile」
# 用例读到父会话的值、假失败。
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  rm -rf "$SANDBOX"
}
trap cleanup EXIT

echo "沙箱: $SANDBOX"
[ -x "$BIN" ] || { echo "先 make build"; exit 1; }

# ---- 假 claude：像 Claude Code 那样发请求。E2E_SCENARIO 选形态 ----
cat > "$FAKEBIN/claude" <<'PY'
#!/usr/bin/env python3
import json, os, urllib.request, urllib.error

def g(k):
    return os.environ.get(k, "")

scenario = os.environ.get("E2E_SCENARIO", "plain")
base, model = g("ANTHROPIC_BASE_URL"), g("ANTHROPIC_DEFAULT_OPUS_MODEL")

TOOLS = [{"name": "Read", "description": "read a file",
          "input_schema": {"type": "object",
                           "properties": {"path": {"type": "string"}}}}]

def post(url, payload):
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("x-api-key", "newgate-local")
    req.add_header("anthropic-version", "2023-06-01")
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        r = op.open(req, timeout=20)
        return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

if scenario == "plain":
    print(f"OPUS_MODEL={model}")
    print(f"SONNET_MODEL={g('ANTHROPIC_DEFAULT_SONNET_MODEL')}")
    print(f"WIN_MAX={g('CLAUDE_CODE_MAX_CONTEXT_TOKENS')}")
    print(f"WIN_COMPACT={g('CLAUDE_CODE_AUTO_COMPACT_WINDOW')}")
    print(f"BASE_URL={base}")
    if base and model:
        code, _ = post(base.rstrip("/") + "/v1/messages",
                       {"model": model, "max_tokens": 16,
                        "messages": [{"role": "user", "content": "ping"}]})
        print(f"HTTP={code}")
elif scenario in ("think1", "think2", "think3"):
    # Claude Code 主循环形态：**流式** + thinking 显式开着 + 带 tools，
    # 历史里的 assistant 消息被客户端剥掉了 thinking 块——只剩 tool_use。
    # （流式是关键：claude-bg 只改写非流式的后台调用，主循环不碰。）
    tid = "toolu_mock_1" if scenario == "think2" else "toolu_UNSEEN_999"
    msgs = [{"role": "user", "content": "go"}]
    if scenario != "think1":
        msgs += [{"role": "assistant", "content": [
                      {"type": "tool_use", "id": tid, "name": "Read",
                       "input": {"path": "x"}}]},
                 {"role": "user", "content": [
                      {"type": "tool_result", "tool_use_id": tid,
                       "content": "ok"}]}]
    code, body = post(base.rstrip("/") + "/v1/messages",
                      {"model": model, "max_tokens": 64, "stream": True,
                       "thinking": {"type": "enabled", "budget_tokens": 1024},
                       "tools": TOOLS, "messages": msgs})
    print(f"HTTP={code}")
    if code != 200:
        print(body[:200])
elif scenario == "count_tokens":
    # Claude Code 的 count_tokens：不带 model 字段的形态也要能活
    code, body = post(base.rstrip("/") + "/v1/messages/count_tokens",
                      {"messages": [{"role": "user", "content": "数一下 token"}],
                       "tools": TOOLS})
    print(f"HTTP={code}")
    try:
        print(f"INPUT_TOKENS={json.loads(body).get('input_tokens')}")
    except Exception:
        print("INPUT_TOKENS=PARSE_FAIL")
elif scenario in ("bg_plain", "bg_adaptive", "bg_other", "bg_realname"):
    # Claude Code 后台小调用的形态（实抓 2026-09，cc 2.1.263）：非流式、
    # model 就是档位名 mid、不带 tools、max_tokens 2112。
    #   bg_plain / bg_adaptive / bg_realname = Bash 分类器本体：system ~126KB
    #     开头是 "You are a security monitor…"（bg_plain 连 thinking 都没写，
    #     bg_adaptive 是客户端设置泄漏成 adaptive）→ 都该切 light + disabled。
    #   bg_other = 其他后台调用（compact 总结这类）：system 没有那句自报
    #     家门 → 保留 mid，只禁思考。
    #   bg_realname = 真实模型名时代的回归现场（2026-09-09 实抓）：分类器
    #     用主循环槽位的真实名（glm-4-plus）点名，它同时绑 heavy+mid、按
    #     Roles 顺序反解成 heavy——tier 闸门版本会整个跳过。仍要切 light。
    payload = {"model": "mid", "max_tokens": 2112,
               "messages": [{"role": "user", "content": "classify this command"},
                            {"role": "user", "content": "and this one"}]}
    if scenario == "bg_realname":
        payload["model"] = "glm-4-plus"
    if scenario != "bg_other":
        payload["system"] = [{"type": "text", "text":
            "You are a security monitor for autonomous AI coding agents."}]
    else:
        payload["system"] = [{"type": "text", "text":
            "Summarize this conversation for context compaction."}]
    if scenario == "bg_adaptive":
        payload["thinking"] = {"type": "adaptive", "budget_tokens": 512}
    code, body = post(base.rstrip("/") + "/v1/messages", payload)
    print(f"HTTP={code}")
    if code != 200:
        print(body[:200])
PY
chmod +x "$FAKEBIN/claude"

# ---- 配置：ds + glm 两个 provider，指向假上游 ----
cat > "$NEWGATE_HOME/providers.json" <<EOF
{
  "providers": {
    "ds":  { "base_url": "http://127.0.0.1:$UP_PORT/v1", "api_key": "sk-ds",  "protocol": "anthropic" },
    "glm": { "base_url": "http://127.0.0.1:$UP_PORT/v1", "api_key": "sk-glm", "protocol": "anthropic" }
  }
}
EOF
cat > "$NEWGATE_HOME/mappings/ds.json" <<'EOF'
{ "name": "ds", "priority": 10, "roles": {
    "heavy": "ds/deepseek-chat", "mid": "ds/deepseek-chat",
    "light": "ds/deepseek-chat", "vision": "ds/deepseek-chat" } }
EOF
cat > "$NEWGATE_HOME/mappings/glm.json" <<'EOF'
{ "name": "glm", "priority": 20,
  "context_window": 1000000, "auto_compact_window": 500000,
  "roles": {
    "heavy": "glm/glm-4-plus", "mid": "glm/glm-4-plus",
    "light": "glm/glm-4.5-air", "vision": "glm/glm-4.5-air" } }
EOF
cat > "$NEWGATE_HOME/state.json" <<EOF
{ "default_profile": "ds", "port": $PROXY_PORT }
EOF

echo; echo "== 1. 启动假上游（严格 DeepSeek 口径） =="
python3 "$ROOT/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "假上游在 127.0.0.1:$UP_PORT" || { bad "假上游没起来"; exit 1; }

# PATH 里 fakebin 在最前，newgate claude 会 exec 我们的假 claude。
export PATH="$FAKEBIN:$PATH"

echo; echo "== 2. newgate claude --profile=ds =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude --profile=ds 2>"$SANDBOX/ds.err")"
echo "$OUT" | sed 's/^/    /'
check "opus 档注入的是真实模型名" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "deepseek-chat"
echo "$OUT" | grep '^BASE_URL=' | grep -q "/a/claude/p/ds" \
  && ok "base URL 带上 /a/claude/p/ds" \
  || bad "base URL 应含 /a/claude/p/ds（实际 $(echo "$OUT" | grep '^BASE_URL=')）"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "上游收到 ds 的 deepseek-chat" "$GOT" "deepseek-chat"

echo; echo "== 3. newgate claude --profile=glm（切换） =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude --profile=glm 2>"$SANDBOX/glm.err")"
echo "$OUT" | sed 's/^/    /'
check "切到 glm 后 opus 档是 glm-4-plus" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "glm-4-plus"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "上游收到 glm 的 glm-4-plus" "$GOT" "glm-4-plus"

echo; echo "== 4. 不带 profile 用默认（ds） =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude 2>"$SANDBOX/default.err")"
check "默认 profile 是 ds（deepseek-chat）" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "deepseek-chat"

echo; echo "== 5. 不存在的 profile 要立刻报错（退出码 65） =="
"$BIN" claude --profile=nope >/dev/null 2>"$SANDBOX/nope.err"
RC=$?
check "退出码 65" "$RC" "65"
grep -q '不存在' "$SANDBOX/nope.err" && ok "报错信息说明了 profile 不存在" || bad "报错信息没说 profile 不存在"

echo; echo "== 6. 上游严格性自检：思考模式下不回传推理必须 400 =="
# 先证明假上游真的在执行官方最严口径——否则后面那些 200 不能说明任何问题。
for body in \
  '{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]}]}' \
  '{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","reasoning_content":"","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]}]}' \
  ; do
  CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
    "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' -d "$body")
  if [ "$CODE" = "400" ] && grep -q "must be passed back" "$SANDBOX/strict.out"; then
    ok "空/缺 reasoning_content → 400（官方口径生效）"
  else
    bad "假上游没拦住空回传（HTTP $CODE）：$(cat "$SANDBOX/strict.out")"
  fi
done

echo; echo "== 7. 思考模式第一轮（客户端剥块场景的起点） =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think1 "$BIN" claude --profile=ds 2>"$SANDBOX/t1.err")"
echo "$OUT" | sed 's/^/    /'
check "think1 首轮 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"

echo; echo "== 8. 第二轮：thinkcache 命中，回填那轮真实推理原文 =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think2 "$BIN" claude --profile=ds 2>"$SANDBOX/t2.err")"
echo "$OUT" | sed 's/^/    /'
check "think2 严格上游放行（回填生效）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
print(m.get("reasoning_content","MISSING"))')
check "reasoning_content 是缓存里的原文" "$GOT" "MOCK-THINKING-ORIGINAL"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
c=m.get("content")
print(c[0].get("thinking","") if isinstance(c,list) and c else "NO_BLOCK")')
check "thinking 块也是缓存里的原文" "$GOT" "MOCK-THINKING-ORIGINAL"

echo; echo "== 9. 缓存未命中（模拟重启后的旧会话）：绝不空串 =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think3 "$BIN" claude --profile=ds 2>"$SANDBOX/t3.err")"
echo "$OUT" | sed 's/^/    /'
check "think3 严格上游放行（占位符非空）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
rc=m.get("reasoning_content","")
c=m.get("content")
blk=c[0].get("thinking","") if isinstance(c,list) and c else ""
def nz(s): return "NONEMPTY" if s.strip() else "EMPTY"
print("rc="+nz(rc)+" blk="+nz(blk))')
check "占位符非空（字段 + 块）" "$GOT" "rc=NONEMPTY blk=NONEMPTY"

echo; echo "== 10. count_tokens：上游听得懂就转发拿真值 =="
# Claude Code 周期性调 count_tokens 算上下文水位（OpenAI 方言上游没有这个
# 端点）。假上游实现了它（原生 anthropic 形态）→ 代理必须转发：model 按
# mid 档链头补上（count_tokens 请求不带 model），客户端拿到上游真值 42，
# 而不是本地字节数/4 粗估。本地粗估兜底（上游 404 → 学习 → 不再白跑）
# 由单测盖着（count_tokens_test.go）。
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=count_tokens "$BIN" claude --profile=ds 2>"$SANDBOX/ct.err")"
echo "$OUT" | sed 's/^/    /'
check "count_tokens 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "拿到上游真值 42（不是本地粗估）" "$(echo "$OUT" | grep '^INPUT_TOKENS=' | cut -d= -f2)" "42"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
ct=[x for x in r if x["path"].endswith("/count_tokens")]
if not ct:
    print("NOT_FORWARDED")
else:
    print(str(ct[0]["body"].get("model","NONE"))+"@"+ct[0]["path"])')
check "转发到了上游、model 按 mid 链头补上" "$GOT" "deepseek-chat@/v1/messages/count_tokens"

echo; echo "== 11. 控制端点：跨用户停机（/__newgate/stop + 令牌） =="
# 多用户部署：claude 用户读得到共享配置，却对 root 起的 daemon 没有
# kill() 权限——停机只能靠这个端点。错令牌必须 403；对令牌 200 且
# daemon 自己退干净（pid/lock 都清掉）。
PIDFILE="$NEWGATE_HOME/.newgate.pid"
if [ -f "$PIDFILE" ]; then
  PORT=$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json"))["port"])')
  TOK=$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json")).get("control_token",""))')
  if [ -n "$TOK" ]; then
    ok "state.json 里生成了控制令牌"
  else
    bad "控制令牌没生成（跨用户停机会退化成「请让 root 来停」）"
  fi
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/__newgate/stop" -H 'Authorization: Bearer wrong-token')
  check "错令牌 → 403" "$CODE" "403"
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/__newgate/stop" -H "Authorization: Bearer $TOK")
  check "对令牌 → 200" "$CODE" "200"
  for _ in $(seq 40); do [ ! -f "$PIDFILE" ] && break; sleep 0.1; done
  if [ ! -f "$PIDFILE" ] && [ ! -f "$NEWGATE_HOME/.newgate.lock" ]; then
    ok "daemon 收到指令后退出，pid/lock 都清了"
  else
    bad "daemon 没退干净（pid 或 lock 还在）"
  fi
  curl -sf "http://127.0.0.1:$PORT/__newgate/status" >/dev/null 2>&1 \
    && bad "端口还在听？" || ok "端口已释放"
else
  bad "沙箱 daemon 的 pid 文件不在（前面哪一步没起 daemon？）"
fi

echo; echo "== 12. 优雅交接：restart 不掐在途流（nginx 式零停机升级） =="
# 上一章把 daemon 停了，先经懒启动路径拉回来。
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
check "懒启动拉回 daemon" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
PIDFILE="$NEWGATE_HOME/.newgate.pid"
read_pid() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pid"])' "$PIDFILE" 2>/dev/null; }
OLD_PID=$(read_pid)
if [ -z "$OLD_PID" ]; then
  bad "pid 文件读不出，没法验证换血"
else
  # 一条 ~2.5s 的慢 SSE 流，中途 restart：流必须完整流完
  curl -sN -o "$SANDBOX/sse.out" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PROXY_PORT/a/claude/p/ds/v1/messages" \
    -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
    -H 'anthropic-version: 2023-06-01' \
    -d '{"model":"deepseek-chat","max_tokens":64,"stream":true,"mock_slow":true,"messages":[{"role":"user","content":"go"}]}' \
    > "$SANDBOX/sse.code" &
  CURL_PID=$!
  sleep 0.8  # 流已经跑起来，正在途中
  OUT="$("$BIN" restart 2>&1)"
  echo "$OUT" | sed 's/^/    /'
  echo "$OUT" | command grep -q '优雅重启' \
    && ok "restart 走了优雅交接（socket 移交）" \
    || bad "restart 没走交接: $(echo "$OUT" | head -2)"
  wait "$CURL_PID"
  check "SSE 流 200（restart 就发生在流中途）" "$(cat "$SANDBOX/sse.code")" "200"
  command grep -q 'message_stop' "$SANDBOX/sse.out" \
    && ok "流完整流完（收到 message_stop）" || bad "在途流被 restart 掐断了"
  RCVD=$(cat "$SANDBOX/sse.out" | command grep -c 'content_block_delta')
  [ "$RCVD" -ge 4 ] && ok "增量块都到了（$RCVD）" || bad "增量块丢了（只有 $RCVD）"
  NEW_PID=$(read_pid)
  if [ -n "$NEW_PID" ] && [ "$NEW_PID" != "$OLD_PID" ]; then
    ok "pid 已换血（$OLD_PID → $NEW_PID）"
  else
    bad "pid 没换血（旧 $OLD_PID，新 '$NEW_PID'）"
  fi
  # 交接后的新请求要落在（也可能没落在，但必须成功）新进程上
  OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
  check "交接后新请求 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
fi

echo; echo "== 13. 后台请求：分类器切轻档，其余只禁思考 =="
# 实抓（2026-09，glm-5.3）：Bash 分类器 = 非流式、model=mid、system 开头
# "You are a security monitor…"、thinking 没写或泄漏成 adaptive → 国模默认
# 思考 15-30 秒，分类器成波超时。代理必须：分类器（bg_plain/bg_adaptive）
# 上游收到 light 模型（glm-4.5-air）+ thinking:disabled；其他后台调用
# （bg_other，compact 总结）保留 mid（glm-4-plus）+ thinking:disabled。
for SC in bg_plain bg_adaptive bg_other bg_realname; do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
  OUT="$(E2E_SCENARIO=$SC "$BIN" claude --profile=glm 2>"$SANDBOX/$SC.err")"
  echo "$OUT" | sed 's/^/    /'
  check "$SC 请求 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
  WANT="glm-4-plus|disabled"          # bg_other：无标记 → 留在 mid
  [ "$SC" != "bg_other" ] && WANT="glm-4.5-air|disabled"  # 分类器 → light
  GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
b=r[0]["body"] if r else {}
th=(b.get("thinking") or {}).get("type","MISSING")
print(str(b.get("model","NONE"))+"|"+th)')
  check "$SC 上游收到 $WANT" "$GOT" "$WANT"
done

echo; echo "== 13b. 分类器改道后，fallback 沿 light 链走（不回 mid） =="
# 改道是路由决策（建链之前）：分类器整条链都是 light。武装一发 500 打掉
# light 头（glm-4.5-air），下一站必须是下一个 profile 的 light
# （ds/deepseek-chat），绝不能掉回 mid 的 glm-4-plus——只换链头、尾巴
# 还是 mid 的旧实现就是这个错。
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
curl -sf "http://127.0.0.1:$UP_PORT/__mock/fail?code=500" >/dev/null
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/bgfo.err")"
echo "$OUT" | sed 's/^/    /'
check "分类器 light 头挂了仍 200（沿链换人）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
models=[x["body"].get("model") for x in r if x["path"].endswith("/messages")]
print("|".join(models))')
check "沿 light 链：glm-4.5-air → deepseek-chat（不回 mid）" "$GOT" "glm-4.5-air|deepseek-chat"

echo; echo "== 15. newgate metrics：路径上的操作全记账 =="
# 计数器在 daemon 内存里（/__newgate/metrics），CLI 经 HTTP 读；第 12 节
# 的 restart 已经清过一次零，所以这里自己先制造几笔再验。count_tokens
# 直接 curl 代理（假上游有这个端点 → 转发拿真值）。
CT_CODE=$(curl -s -o "$SANDBOX/ct2.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/glm/v1/messages/count_tokens" \
  -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
  -d '{"messages":[{"role":"user","content":"count me"}]}')
check "metrics 前置：count_tokens 200" "$CT_CODE" "200"
MOUT="$("$BIN" metrics 2>"$SANDBOX/metrics.err")"
echo "$MOUT" | sed 's/^/    /'
for KEY in "special.claude-bg.route_light" "count_tokens.forwarded" "count_tokens.total" "chain.step_failed" "chain.failover"; do
  echo "$MOUT" | command grep -q "$KEY" \
    && ok "metrics 有 $KEY" \
    || bad "metrics 缺 $KEY（输出：$(echo "$MOUT" | head -3)）"
done

echo; echo "== 14. 窗口声明：声明了才注入，跟被选中的 profile 走 =="
# Claude Code 不认识我们注入的真实模型名，按「未知模型」默认 200k 窗口
# 提前 compact（2026-09 实测 glm-5.3 会话 125k 就在 compact）。glm 声明
# 了 1M/500k → 两个 env 都注入；ds 没声明 → 一个都不注入。
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=glm 2>/dev/null)"
check "glm: MAX_CONTEXT_TOKENS=1000000" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" "1000000"
check "glm: AUTO_COMPACT_WINDOW=500000" \
  "$(echo "$OUT" | grep '^WIN_COMPACT=' | cut -d= -f2-)" "500000"
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
check "ds（没声明）: MAX_CONTEXT_TOKENS 为空" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" ""
check "ds（没声明）: AUTO_COMPACT_WINDOW 为空" \
  "$(echo "$OUT" | grep '^WIN_COMPACT=' | cut -d= -f2-)" ""

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ]
