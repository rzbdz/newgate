#!/usr/bin/env bash
# 零 token 端到端：`newgate claude --profile=ds|glm` 的 ds↔glm 切换，
# 加上 Claude Code 形态的思考模式多轮对话。
#
# 验证（对应本次特性/修复）：
#   1. 启动时注入的 env 是**真实模型名**（claude 界面显示 deepseek-chat，
#      而不是 heavy），且 base URL 带上 /a/claude/p/<profile>。
#   2. 代理把真实模型名反解回档位，转发给正确的上游。
#   3. DeepSeek 思考模式：Claude Code 会把 thinking 块剥掉，代理补回
#      thinkcache 里那轮的真实原文（tool id 找回）；**查不到就一个字节都不补**
#      （2026-09-18 起：上游自己没给过推理，我们凭什么替它编）。假上游按
#      **实测口径**校验——见第 6 章，判据是**尾部形状**不是推理字段。
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-claude-e2e.XXXXXX)}"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18081}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18898}"
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
  # 调试用：NEWGATE_E2E_KEEP=1 保留沙箱（日志、假上游收到的 body 都在里面），
  # 出红的时候不至于对着一个删掉的目录猜。
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "沙箱保留（NEWGATE_E2E_KEEP）: $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
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
    #     bg_adaptive 显式要求 adaptive）→ 都切 light；只给没写 thinking 的
    #     bg_plain 注入 disabled，显式意图不覆盖。
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

echo; echo "== 4. 不带 profile 用默认（ds）：动态模式，槽位 = 档位名 =="
# 默认（动态）模式槽位保持档位名——会话发档位名回来，代理按请求时的当前
# 配置解析，--set-profile 对跑着的会话立刻生效。窗口声明照注入（档位名
# 对 Claude Code 也是未知模型，MAX_CONTEXT_TOKENS 一样生效）。
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude 2>"$SANDBOX/default.err")"
check "默认（动态）：opus 槽 = 主力档 normal" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "normal"
check "默认（动态）：窗口声明照注入（ds 没配 → 空）" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" ""

echo; echo "== 5. 不存在的 profile 要立刻报错（退出码 65） =="
"$BIN" claude --profile=nope >/dev/null 2>"$SANDBOX/nope.err"
RC=$?
check "退出码 65" "$RC" "65"
grep -q '不存在' "$SANDBOX/nope.err" && ok "报错信息说明了 profile 不存在" || bad "报错信息没说 profile 不存在"

echo; echo "== 6. 上游严格性自检：尾部形状（不是推理字段）才决定 400 =="
# 先证明假上游真的在执行**实测口径**——否则后面那些 200 不能说明任何问题。
#
# 判据是 2026-09-18 打真实 smt-deepseek/deepseek-flash 逐格实测出来的，而且
# 是拿**真实 Claude Code 抓包**（dump/err-400-req000412.client-sent.json，
# 764KB、229 条消息、UA claude-cli/2.1.273）原样复现的：
#
#   原样（尾 = 裸 [tool_result]）                     → 400 reasoning_content…
#   同一个请求体，只去掉最后 assistant 的 thinking 块  → 400（推理字段无关）
#   同一个请求体，只在尾部 content[] 里加 text " "     → 200
#   两个都做                                          → 200
#
# 报错文案说「推理没回传」，真实原因却是「这一轮没有新指令」——文案与原因
# 不一致，正是这条检查必须按**形状**写、不能按文案写的原因。
#
# 三格：裸 tool_result 要 400；同一个 + 一个空格 text、以及 + 一个普通文字块
# 都要 200。上下两格一起才说明判据画在哪条线上（只测 400 会让「见谁都拦」
# 的假上游也过）。
tail_case() {  # $1=尾部 content[] 的字面量  $2=期望状态码  $3=说明
  CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
    "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' \
    -d '{"model":"deepseek-chat","max_tokens":16,"thinking":{"type":"enabled","budget_tokens":1024},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"跑一下"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]},{"role":"user","content":'"$1"'}]}')
  check "尾部 $3 → $2" "$CODE" "$2"
}
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"}]' \
  400 "只有 tool_result（裸）"
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"},{"type":"text","text":" "}]' \
  200 "tool_result + 一个空格"
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"},{"type":"text","text":"继续"}]' \
  200 "tool_result + 普通指令"
# 报错原文必须是上游那句（后面几章靠它认形状），逐字锁住。
CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
  "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}')
command grep -q "must be passed back" "$SANDBOX/strict.out" \
  && ok "400 的文案是上游那句（推理字段）——文案与真实原因不一致，别按文案判" \
  || bad "400 文案不对：$(head -c 160 "$SANDBOX/strict.out")"

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

echo; echo "== 9. 缓存未命中（模拟重启后的旧会话）：一个字节都不补 =="
# 这一章锁的是 2026-09-18 定下来的那条**最基本**的逻辑：上游自己那一轮就没
# 给过推理，那「must be passed back」要求回传的东西根本不存在，我们凭什么
# 替它编一个。原来这里补的是 "No thinking in this round"，是错的：
# 编出来的字会进上游、进对话历史、每轮烧 token，而信息量是零。
#
# 正确动作是**跳过这条消息**，并且把「为什么没有原文」分好类报进日志
# （notool / nocache / nokey）——跳过是结果，光报个数没法反查。
#
# 但**尾部形状**那一手（第 4 手）在这一发上照样要动：think3 的历史正好是
# 「裸 tool_result 收尾」，上游对那个形状报的正是这条 400。两件事互不冲突，
# 所以这里同时断言：
#   assistant 消息：rc 缺席、没有 thinking 块（不编）
#   尾部 user 轮：多了一条「继续」（修形状）
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think3 "$BIN" claude --profile=ds 2>"$SANDBOX/t3.err")"
echo "$OUT" | sed 's/^/    /'
check "think3 严格上游放行（尾部形状被修好）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
msgs=r[0]["body"]["messages"] if r else []
a=[m for m in msgs if m.get("role")=="assistant"]
c=(a[0].get("content") if a else None) or []
blk=[b.get("type") for b in c if isinstance(b,dict)]
rc="PRESENT" if (a and "reasoning_content" in a[0]) else "ABSENT"
lastu=[m for m in msgs if m.get("role")=="user"][-1].get("content")
tailb=[b.get("type") for b in lastu] if isinstance(lastu,list) else [lastu]
print("rc="+rc+" blocks="+",".join(blk)+" tail="+",".join(tailb))')
check "不编占位符（rc 缺席、无 thinking 块），但尾部形状被修" \
  "$GOT" "rc=ABSENT blocks=tool_use tail=tool_result,text"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
lastu=[m for m in r[0]["body"]["messages"] if m.get("role")=="user"][-1]["content"]
print([b.get("text") for b in lastu if isinstance(b,dict) and b.get("type")=="text"][0])')
check "追加的就是最简那句「继续」" "$GOT" "继续"
# 跳过的原因必须分类报出来（这条 tool_use id 从没进过 thinkcache ⇒ nocache）。
LOGTAIL=$(tail -40 "$NEWGATE_HOME/newgate.log")
case "$LOGTAIL" in
  *"跳过不动"*"nocache"*) ok "日志把「没有原文可补」的原因分成 nocache 并说明跳过" ;;
  *) bad "日志没说清跳过原因（该有 \"跳过不动\" + \"nocache\"）：$(echo "$LOGTAIL" | command grep 'assistant 消息' | tail -2)" ;;
esac

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
  # 等的是「pid 和 lock 都清了」——断言的就是这两件事，等待条件必须一致。
  # 只等 pidfile 会在 RemovePid() 与 RemoveLock() 之间被抢占：pid 没了、lock
  # 还剩几微秒，轮询恰好落进这个窗口就误报「没退干净」（2026-09-17 偶发）。
  for _ in $(seq 40); do
    [ ! -f "$PIDFILE" ] && [ ! -f "$NEWGATE_HOME/.newgate.lock" ] && break
    sleep 0.1
  done
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
# "You are a security monitor…"。代理必须把分类器（bg_plain/bg_adaptive）
# 切到 light；没写 thinking 的 bg_plain 注入 disabled，显式 adaptive 必须
# 保留。其他后台调用（bg_other，compact 总结）保留 mid + disabled。
for SC in bg_plain bg_adaptive bg_other bg_realname; do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
  OUT="$(E2E_SCENARIO=$SC "$BIN" claude --profile=glm 2>"$SANDBOX/$SC.err")"
  echo "$OUT" | sed 's/^/    /'
  check "$SC 请求 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
  WANT="glm-4-plus|disabled"          # bg_other：无标记 → 留在 mid
  [ "$SC" != "bg_other" ] && WANT="glm-4.5-air|disabled"  # 分类器 → light
  [ "$SC" = "bg_adaptive" ] && WANT="glm-4.5-air|adaptive" # 显式 thinking 不覆盖
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

echo; echo "== 16. naked：Bash 分类器被短路，上游一个请求都收不到 =="
# 裸奔是「自家安全门」：开着时 Bash 分类器请求被直接批准，不经过上游。判据
# 跟上章节同一个（bg_plain = "You are a security monitor…" 的后台小调用）。
#   关着：请求真的打到上游（glm 的 light 档）；
#   开着：请求被短路，上游 /__mock/requests 数 0，短路口计数器涨。
UPCOUNT() { curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))'; }
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }

RESETUP
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/nk_off.err")"
echo "$OUT" | sed 's/^/    /'
check "裸奔关：bg_plain 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "裸奔关：上游收到 1 个分类器请求" "$(UPCOUNT)" "1"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
print(r[0]["body"].get("model","NONE") if r else "NONE")')
check "裸奔关：分类器照常切到 light（glm-4.5-air）" "$GOT" "glm-4.5-air"

"$BIN" naked on >/dev/null 2>&1
RESETUP
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/nk_on.err")"
echo "$OUT" | sed 's/^/    /'
check "裸奔开：bg_plain 仍 200（被短路批准）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "裸奔开：上游收到 0 个请求（真的被短路了）" "$(UPCOUNT)" "0"
MOUT="$("$BIN" metrics 2>/dev/null)"
# metrics 表格把长计数器名**截断**显示（special.classifier-naked.shortcircuit
# 截成 special.classifier-naked.shor），所以按短名 + 那行的人话 hint 一起判。
echo "$MOUT" | command grep -q "classifier-naked" \
  && echo "$MOUT" | command grep -q "分类器请求被直接批准" \
  && ok "metrics 有 special.classifier-naked.shortcircuit（短路已计数）" \
  || bad "metrics 缺 naked 短路计数（输出：$(echo "$MOUT" | command grep -a classifier-naked)）"
"$BIN" naked off >/dev/null 2>&1
check "裸奔终于关掉（不留沙箱脏状态）" "$("$BIN" naked 2>&1 | command grep -c '已关闭')" "1"

echo; echo "== 17. 形状 400：上游 400 原样透传 + 熔断器只计数、永不摘牌 =="
# 这份 body 要满足两个条件，缺一条这个用例就不是它要测的东西：
#
#   1. **尾部形状违规、而且插件修不了**。实测判据是「最后一条 role:user 的
#      content[] 里全是 tool_result 块」（详见第 6 章与 mock/fake_upstream.py），
#      而 deepseek 插件对其中**修得好**的那一族（那个 user 轮就是数组末尾）
#      会追加一句「继续」把它修掉、返回 200——那是第 9 章在锁的事。
#      所以这里用的是**修不好**的那一族：裸 tool_result 之后**还有一条
#      assistant**。插件故意不碰它（往用户的对话里塞一句模型看不见效果的
#      噪音比 400 更糟），实测这一族在原样追加「继续」之后 3/3 还是 400。
#      于是链上**每一个**候选都 400，链走到头，客户端才拿得到上游原文。
#   2. **走 glm profile**，让 glm 那一发先吃 400：deepseek 插件的 MatchTarget
#      看 model/provider/baseURL，glm/glm-4-plus 三处都没有「deepseek」字样，
#      于是插件不去碰它。（这一点是冗余保险：就算 Match 判错，条件 1 也兜住了。）
#
# 形状判据（modules/deepseek/shape.go）认领它 → classify 判 BucketShape →
# 账本只涨 ShapeSkips、永不进 Open。所以「客户端拿到 400」和「glm 没被摘牌」
# 必须**同时**成立：这正是这轮重构要的那个行为。
RESETUP
SHAPE_BODY='{"model":"glm-4-plus","max_tokens":16,"stream":true,'\
'"thinking":{"type":"enabled","budget_tokens":1024},'\
'"tools":[{"name":"Bash","description":"d","input_schema":{"type":"object","properties":{}}}],'\
'"messages":['\
'{"role":"user","content":[{"type":"text","text":"跑一下"}]},'\
'{"role":"assistant","content":[{"type":"tool_use","id":"toolu_shape_17","name":"Bash","input":{}}]},'\
'{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_shape_17","content":"ok"}]},'\
'{"role":"assistant","content":[{"type":"text","text":"好"}]}]}'

# (1) 直打假上游：证明这条规则真的部署到位（不是被代理偷偷改过）。
CODE=$(curl -s -o "$SANDBOX/shape_direct.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$UP_PORT/v1/messages" \
  -H 'Content-Type: application/json' -d "$SHAPE_BODY")
check "直打上游：400 must be passed back" \
  "$( [ "$CODE" = "400" ] && command grep -q 'must be passed back' "$SANDBOX/shape_direct.out" && echo y || echo n)" "y"

# (2) 同 body 经代理：链上每一站都 400 → 客户端必须收到上游原文（不静默）。
RESETUP
RES_CODE=$(curl -s -o "$SANDBOX/shape_proxy.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/glm/v1/messages" \
  -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' -H 'x-api-key: e2e' -d "$SHAPE_BODY")
check "经代理：客户端仍 400" "$RES_CODE" "400"
command grep -q 'must be passed back' "$SANDBOX/shape_proxy.out" \
  && ok "经代理：上游原文原样透传（不静默）" \
  || bad "经代理：被吞了；body=$(head -c 200 "$SANDBOX/shape_proxy.out")"

# 日志里那行 [shape-400] 是判据认领的**唯一**证据（转发路径不认识任何上游
# 专有字符串，它只读 Result.Shape 那个名字）。
LOG="$NEWGATE_HOME/newgate.log"
for _ in $(seq 20); do
  command grep -q '\[shape-400\]' "$LOG" 2>/dev/null && break; sleep 0.1
done
command grep -aq '\[shape-400\].*判据 deepseek' "$LOG" \
  && ok "日志有 [shape-400] 判据 deepseek（认领留痕）" \
  || bad "日志里没有 [shape-400] 判据 deepseek"

# (3) 熔断器只计数、绝不摘牌。`newgate breaker` 把问题 binding 分两段，这条
#     shape-400 只能出现在「只计数、没摘牌」那一段。
BRK_OUT="$("$BIN" breaker 2>/dev/null)"
echo "$BRK_OUT" | sed 's/^/    /'
echo "$BRK_OUT" | command grep -q '只计数、没摘牌' \
  && ok "breaker 表里有「只计数、没摘牌」段（shape-400 的归属）" \
  || bad "breaker 表里找不到「只计数、没摘牌」段"
echo "$BRK_OUT" | command grep -q 'glm/glm-4-plus' \
  && ok "breaker 表里能找到 glm/glm-4-plus（被记账了）" \
  || bad "breaker 表里找不到 glm/glm-4-plus"
echo "$BRK_OUT" | command grep -qE '· 0 个被摘牌' \
  && ok "没有任何 binding 被摘牌（形状 400 只计数）" \
  || bad "有 binding 被摘牌了（形状 400 不该摘牌）：$(echo "$BRK_OUT" | command grep '被摘牌' | head)"

# (4) metrics 端的形状计数要涨。
"$BIN" metrics 2>/dev/null | command grep -q 'breaker.skipped.shape_error' \
  && ok "metrics 有 breaker.skipped.shape_error" \
  || bad "metrics 缺 breaker.skipped.shape_error"

echo; echo "== 18. 补丁侧的三个契约：不编、措辞最小、原因分类 =="
# 这一章锁 2026-09-18 定下来的三件事，全在 deepseek 插件的改写路径上。
# 这里直打**代理**、读假上游收到的 body：这些是纯字节手术，假上游收到的
# 就是上游会收到的字节。
#
# (a) **绝不编**。上游那一轮没给过推理，那「must be passed back」要求回传的
#     东西根本不存在，我们凭什么替它编一个——编出来的字会进上游、进对话
#     历史、每轮烧 token，信息量是零。原来补的是 "No thinking in this round"，
#     现在是**跳过这条消息，一个字节都不加**。这条请求里那条 assistant 的
#     tool_use id 从没进过 thinkcache ⇒ 没有原文 ⇒ 必须原样不动。
#
# (b) **尾部修复的措辞最小**。第 4 手往尾部追加的那句话会进上游、也会进用户
#     下一轮的对话历史，越长越像「有人在替我说话」。用户的原话是「只用最少字，
#     比如（"继续"）这种」。所以断言的是**逐字**等于「继续」，不是「非空且短」
#     ——后者换个长句子照样能过。
#
# (c) **原因必须分类报出来**。跳过是**结果**，光报个数没法反查：是「上游本来就
#     没给」还是「给了但我们没存住」，处置完全相反（前者只能接受，后者要去
#     查 thinkcache 的命中率）。分三类：
#       notool   纯文本轮，靠正文哈希找回，miss
#       nocache  有 tool_use，但它的 id 在 thinkcache 里查不到
#       nokey    既无 tool_use 也无正文，认不出这条消息
#     这条请求的 tool_use id 从没被缓存过，所以必然是 nocache。
#
# 请求里显式写 reasoning_content「这一轮本来就有推理，插件不许碰」：插件只补
# **缺**的字段，已经有了的一个字节都不碰（既有契约）。所以同一条请求同时给出
# 两个样本：第一条 assistant 有原文（不许碰）、尾部那个 user 轮是裸 tool_result
# （必须修）。
PH_BODY='{"model":"deepseek-chat","max_tokens":16,"stream":false,'\
'"thinking":{"type":"enabled"},'\
'"messages":['\
'{"role":"user","content":[{"type":"text","text":"跑一下"}]},'\
'{"role":"assistant","reasoning_content":"这一轮本来就有推理，插件不许碰",'\
'"content":[{"type":"text","text":"好"},{"type":"tool_use","id":"t-never-cached-e2e","name":"Bash","input":{}}]},'\
'{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-never-cached-e2e","content":"ok"}]}]}'

RESETUP
CODE=$(curl -s -o "$SANDBOX/ph_proxy.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/ds/v1/messages" \
  -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' -H "x-api-key: e2e" -d "$PH_BODY")
check "经代理：思考开着、尾部裸 tool_result → 200（形状修好了）" "$CODE" "200"

# 逐条把契约打成一行一行的 key=value，再一条条 check——不用 read 拆词
# （read -r A B 是按空白切分，正文里带空格就错位），也不做子串匹配。
PH_SUM=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
if not r: print("NO_REQUEST"); raise SystemExit
msgs=r[-1]["body"]["messages"]
a=[m for m in msgs if m.get("role")=="assistant"][0]
blocks=a.get("content") or []
if not isinstance(blocks,list): print("NOT_ARRAY"); raise SystemExit
kinds=[b.get("type") for b in blocks if isinstance(b,dict)]
texts=[b.get("text","") for b in blocks if isinstance(b,dict) and b.get("type")=="text"]
lu=[m for m in msgs if m.get("role")=="user"][-1].get("content")
tail=[b.get("type") for b in lu] if isinstance(lu,list) else ["STR"]
tailtext=[b.get("text","") for b in lu if isinstance(b,dict) and b.get("type")=="text"] if isinstance(lu,list) else []
print("thinking_blocks=" + str(kinds.count("thinking")))
print("reasoning_content=" + a.get("reasoning_content","<ABSENT>"))
print("tail=" + ",".join(tail))
print("tail_text=" + (tailtext[0] if tailtext else "<NONE>"))')
echo "$PH_SUM" | sed 's/^/    /'
ph_get() { echo "$PH_SUM" | command grep "^$1=" | cut -d= -f2-; }

# (a) 绝不编：这条 assistant 没有原文 ⇒ 不插 thinking 块、不加 reasoning_content。
check "没有原文 ⇒ 不插 thinking 块（不编占位符）" "$(ph_get thinking_blocks)" "0"
check "没有原文 ⇒ 已经写着的 reasoning_content 逐字不动" \
  "$(ph_get reasoning_content)" "这一轮本来就有推理，插件不许碰"
# (b) 尾部形状被修好，而且追加的是**逐字**那句最简指令。
check "尾部从裸 tool_result 变成 tool_result+text" "$(ph_get tail)" "tool_result,text"
check "追加的指令逐字是「继续」（最少字）" "$(ph_get tail_text)" "继续"

# (c) 原因分类。日志里那句必须同时说清「跳过了」和「为什么」（nocache）。
LOGTAIL=$(tail -60 "$NEWGATE_HOME/newgate.log")
case "$LOGTAIL" in
  *"跳过不动"*"nocache"*) ok "日志说明跳过、且把原因分成 nocache（有 tool_use、缓存查不到）" ;;
  *) bad "日志没说清跳过原因（该有 \"跳过不动\" + \"nocache\"）：$(echo "$LOGTAIL" | command grep 'assistant 消息' | tail -2)" ;;
esac
case "$LOGTAIL" in
  *"只能补占位符"*) bad "日志里还有「占位符」字样——编占位符这条路应该已经删掉了" ;;
  *) ok "日志里不再出现「占位符」（那条路已删）" ;;
esac

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ]
