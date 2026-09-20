#!/usr/bin/env bash
# 零 token 端到端：**内核自己**的行为——接管注入、档位解析与转发、控制端点、
# 优雅交接、后台分类器改道、窗口声明、运行期开关。
#
# 验证（对应本次特性/修复）：
#   1. 启动时注入的 env 是**真实模型名**（claude 界面显示 deepseek-chat，
#      而不是 heavy），且 base URL 带上 /a/claude/p/<profile>。
#   2. 代理把真实模型名反解回档位，转发给正确的上游。
#   3. count_tokens：Claude Code 周期性调用（水位条/自动压缩阈值）。上游
#      听得懂（原生 anthropic 端点）就转发拿真值、model 按 mid 链头补上；
#      听不懂的（聚合器 404）由 forward 层 lazy probe 学下来退回本地粗估
#      （单测覆盖）。
#   4. 控制端点 /__newgate/stop：多用户共享部署下，读得到配置却发不出
#      信号的用户靠它停机——错令牌 403，对令牌让 daemon 退干净。
#   5. 优雅交接 /__newgate/upgrade（nginx 式零停机升级）：restart 把监听
#      socket 移交给新进程，在途 SSE 流由旧进程流完为止——开发 newgate
#      的会话本身就穿行在代理里，这是「能持续开发」的前提。
#   6. 后台请求：分类器整条链改走 light、其余只禁思考。Claude Code 的非流
#      式后台调用不带 thinking，国模却把缺省当默认思考 → 15-30 秒、成波超
#      时。代理一律补 thinking:disabled（缺就补、带了也改写）；其中 Bash
#      安全分类器本体（实抓特征：system 开头 "You are a security monitor…"）
#      在**建链之前**改道 light 档——含 fallback，light 挂了沿 light 链换
#      人，不回 mid；其他后台调用（compact 总结这类）不改道；主循环的流式
#      请求不受影响。
#   7. 窗口声明：Claude Code 不认识注入的真实模型名（glm-4-plus），按
#      「未知模型」默认 200k 窗口提前 compact。profile 里声明了
#      context_window/auto_compact_window 就在启动时注入对应的
#      CLAUDE_CODE_* env；没声明的 profile 一个都不注入。
#   8. 运行期开关与 special 层开关（第 19/20 章）：探针用内核自己的
#      schema-repair，这样这一层不依赖任何发行版模块。
#
# # 发行版模块的行为不在这里（2026-09-20 拆开）
#
# 上游怪癖补丁（DeepSeek 的推理原文回填、尾部形状修补、跨上游迁移）跟着模块
# 住在发行版仓库，由 **发行版的 mock/e2e_claude_dist.sh** 锁——它复用本目录的
# 假上游（那是逐字节复刻真实上游行为的产物，复制一份必然漂移）。拆开之前那几
# 章在本文件里，于是**内核的测试依赖一个发行版模块**：摘掉它内核就红。边界与
# 理由见 docs/09-extension-guide.md §8。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config / ~/.claude / shell rc。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/newgate"
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-claude-e2e.XXXXXX)}"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18081}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18898}"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
mkdir -p "$NEWGATE_HOME/mappings"
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

# 代理由内核自己的 runtime 命令拉起来。2026-09-20 之前这里是隐式的——客户端那几章
# （`newgate claude …`）会顺带把 daemon 起起来；那些章节跟着 claudecode 搬去发行版
# 之后，这里就得显式起，否则后面每一章都打在空气上（实测：pid 文件不在 → 全红）。
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }
"$BIN" start >/dev/null 2>&1

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

# 第 11 章把 daemon 停掉了（那正是它的目的）。以前这里靠第 12 章的 restart 顺手拉回来，
# 那一章跟着客户端搬去发行版了，所以显式起回来——下面两章都打在代理上。
"$BIN" start >/dev/null 2>&1

echo; echo "== 15. newgate metrics：路径上的操作全记账 =="
# 计数器在 daemon 内存里（/__newgate/metrics），CLI 经 HTTP 读；第 12 节
# 的 restart 已经清过一次零，所以这里自己先制造几笔再验。count_tokens
# 直接 curl 代理（假上游有这个端点 → 转发拿真值）。
CT_CODE=$(curl -s -o "$SANDBOX/ct2.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/p/glm/v1/messages/count_tokens" \
  -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
  -d '{"messages":[{"role":"user","content":"count me"}]}')
check "metrics 前置：count_tokens 200" "$CT_CODE" "200"
MOUT="$("$BIN" metrics 2>"$SANDBOX/metrics.err")"
echo "$MOUT" | sed 's/^/    /'
# 指标清单里曾经有 special.claude-bg.route_light（claude-bg 改道的那笔账）——
# 那个插件跟着 claudecode 搬去发行版了，它的指标由发行版的 e2e 断言。
# chain.step_failed / chain.failover 也在这张清单里过——它们由「链上换站」的现场产生，
# 而那些现场（分类器改道、上游 400 换人）跟着客户端章节搬去了发行版。内核这一侧改为
# 断言「记账机制本身在工作」：这两笔账的**产生**由发行版的 e2e 覆盖。
for KEY in "count_tokens.forwarded" "count_tokens.total"; do
  echo "$MOUT" | command grep -q "$KEY" \
    && ok "metrics 有 $KEY" \
    || bad "metrics 缺 $KEY（输出：$(echo "$MOUT" | head -3)）"
done

echo; echo "== 19. 运行期开关：模块自己上报、命令自己注册 =="
# 这一层是「everything is module」的用户界面：模块在 Start 里把自己的开关点
# 上报给 plugin-manager（RegisterSelf），命令由**各模块自己**注册进 cli
# （gateway 的 st/schema-repair、claudecode 的 naked、plugin-manager 的 plugin）。
#
# 这里断言的是列表的地基：**枚举源必须是组件图**，不是「谁上报过」——否则
# 「这个模块没有开关」和「这个模块忘了注册」长得一模一样，用户看到的是同一个
# 空列表。「关掉一个点真的改变热路径」那一半由第 20 章的 schema-repair 锁。
#
# 2026-09-20 起这一章只看**内核自带**的模块：上游怪癖模块（deepseek 那套开关点
# 曾经是这一层最好的例子）跟着发行版走了，它的开关点由发行版自己的 e2e 锁。
PLUGIN_OUT="$("$BIN" plugin 2>&1)"
case "$PLUGIN_OUT" in
  *"breaker"*"plugin-manager"*"gateway"*)
    ok "plugin：列出全部模块（含没参与开关体系的）" ;;
  *) bad "plugin 没列全模块：$(echo "$PLUGIN_OUT" | head -6 | tr '\n' ' ')" ;;
esac
case "$PLUGIN_OUT" in
  *"infra"*"gateway"*"model"*) ok "plugin：按分类分组展示" ;;
  *) bad "plugin 没有按分类分组" ;;
esac
case "$PLUGIN_OUT" in
  *"无法 runtime 开关（v1）"*)
    ok "plugin：没上报开关点的模块显式标注（不是静默省略）" ;;
  *) bad "plugin 没标注「无法 runtime 开关」" ;;
esac

# 开关点认不出来时必须是**报错**，不是静默当成 on/off。
"$BIN" plugin 根本没有这个模块 off >/dev/null 2>&1
check "plugin：不存在的目标以非零退出" "$([ $? -ne 0 ] && echo yes || echo no)" "yes"

echo; echo "== 20. special_treatment（整层 / 单插件）与 schema-repair =="
# 这些开关不住在 plugin-manager 的账本里，而是 domain.State 上的 typed 字段
# （special_treatment / special_treatment_off / schema_repair），所以单开一章。
# 断言的是同一件事：**关掉之后热路径真的不做了**——只写进 state.json 而没人读，
# 等于一个好看的开关。
#
# 探针用 schema-repair（内核 gateway 自己的一个 special 插件）：它补的是
# "required": []，语义无操作，但**上游收到的字节里有没有那个键是看得见的**，
# 正好当「这一层还在不在跑」的探针。2026-09-20 之前这里的探针是发行版那支
# 尾部形状补丁（deepseek 第 4 手）——它更好用，但它跟着拥有者搬去发行版了。
SCHEMA_BODY='{"model":"normal","max_tokens":16,"messages":[{"role":"user","content":"hi"}],'\
'"tools":[{"type":"function","function":{"name":"t","parameters":{"type":"object","properties":{}}}}]}'
has_required() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
try:
    r=json.load(sys.stdin)
    prm=r[0]["body"]["tools"][0]["function"]["parameters"]
    print("有" if "required" in prm else "无")
except Exception:
    print("NOUP")' 2>/dev/null
}
post_schema() {
  RESETUP
  curl -s -o /dev/null -X POST "http://127.0.0.1:$PROXY_PORT/p/ds/v1/chat/completions" \
    -H 'Content-Type: application/json' -d "$SCHEMA_BODY"
}

# (1) 单插件开关：这一格就是「只写进 state.json 而没人读」的反面。
"$BIN" schema-repair on >/dev/null 2>&1
post_schema
check "schema-repair on ⇒ 上游收到补好的 required" "$(has_required)" "有"

"$BIN" schema-repair off >/dev/null 2>&1
post_schema
check "schema-repair off ⇒ 字节原样，不补 required" "$(has_required)" "无"

check "收尾：开关都回到出厂态" \
  "$("$BIN" status 2>&1 | command grep -c 'special 关了\|schema repair off')" "0"


echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ]
