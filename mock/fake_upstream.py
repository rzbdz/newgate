#!/usr/bin/env python3
"""假上游：冒充 your-gateway 网关，零 token。

记录收到的一切，好让我们断言 newgate 到底改写成了什么模型、带了哪个 key。

  python3 mock/fake_upstream.py --port 18080 [--log requests.jsonl]

路由：
  POST /v1/chat/completions   openai 方言（支持 stream）
  POST /v1/messages           anthropic 方言（支持 stream）
  GET  /__mock/requests       返回收到的全部请求（JSON）
  POST /__mock/reset          清空记录
  GET  /__mock/fail?code=429  下一个请求返回该状态码
"""
import argparse, json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

RECORDED = []
NEXT_FAIL = {"code": None}

# 上游那条 400 的真实判据。**2026-09-18 按实测重写**：原来这里照抄的是官方文档
# 口径（「带 tools 且思考开着时，历史里每条 assistant 都必须回传非空推理」），
# 那条口径在真实上游上根本不成立——打真实 smt-deepseek/deepseek-flash 逐格实测
# （每格 3/3，见 modules/deepseek/st-reasoning.go 文件头与 docs/06-reasoning.md §2b）：
#
#   reasoning_content 的形态（真实原文 / 省略字段 / 空串 / 占位符）**全都不影响结果**
#   唯一起作用的是**尾部形状**：
#     最后一条 user 消息的 content[] 全是 tool_result 块  → 400
#     同一个 content[] 里多一个非空 text 块（哪怕只有一个空格）→ 200
#
# 报错文案仍是上游那句：它把「你这轮没有新指令」报成了「推理没回传」。文案与
# 真实原因不一致，是这个上游最坑的地方，也是这条检查必须按形状写而不是按文案写
# 的原因。
#
# 另外两条实测到、但本函数**故意不模拟**的：text 块空串会得到 `missing field
# text`；数组以 assistant 收尾是另一条规则。插件不修这两族，假上游也就不拦。
STRICT_REASONING_ERR = {
    "type": "error",
    "error": {
        "type": "invalid_request_error",
        "message": "The `reasoning_content` in the thinking mode must be "
                   "passed back to the API. (request id: mock-e2e-strict)",
    },
}


def strict_reasoning_violation(body):
    """有违规返回 True。判据是**尾部形状**，不是推理字段——见上面那段实测记录。"""
    if not isinstance(body, dict):
        return False
    messages = body.get("messages")
    if not isinstance(messages, list):
        return False
    last_user = None
    for m in messages:
        if isinstance(m, dict) and m.get("role") == "user":
            last_user = m
    if last_user is None:
        return False
    blocks = last_user.get("content")
    if not isinstance(blocks, list) or not blocks:
        return False  # content 是字符串 = 本来就有文字，放行
    return all(isinstance(b, dict) and b.get("type") == "tool_result"
               for b in blocks)


# Responses 方言（codex 走的那条）对 custom 工具的两句 400。**2026-09-21 按实测写**：
# 打真实 smt-deepseek/deepseek-flash，把 custom 工具放进工具树里逐格试过——
#
#   挂在顶层       → Unsupported custom tool: 'exec'. Only 'apply_patch' is supported.
#   挂在 namespace 里 → Currently custom tools are not allowed inside a namespace.
#                      Found custom tool 'exec' in tools[1].tools[0].
#
# 两句都是**请求形状**问题（apply_patch 是这一家唯一认的 custom），所以这里照实拦：
# 假上游要复刻的是真实上游的判据，替它容错就等于把这条 e2e 变成绿的假测试。
#
# 两句的**信封**在真实上游上并不一致（同一家不同的后端节点给过两种 code 与措辞
# 尾巴），所以这里只逐字复刻那句**话**——话是判据，信封不是。
#
# 一格按同族推、没有实测：namespace 里的 apply_patch。本函数按「放行」处理（与顶层
# 一致）。这一格在产品侧不可达（降级会把非 apply_patch 的 custom 全改掉，而
# apply_patch 本来也不该被塞进 namespace），留着只是为了别把「不确定」写成假的确定。
RESPONSES_CUSTOM_TOOL_ERR = "Unsupported custom tool: '{name}'. Only 'apply_patch' is supported."
RESPONSES_NESTED_CUSTOM_ERR = ("Currently custom tools are not allowed inside a "
                               "namespace. Found custom tool '{name}' in tools[{i}].tools[{j}].")


def responses_custom_violation(body):
    """有违规返回那句 400 的文案，没有返回 None。判据 = 真实上游那两句（见上）。"""
    if not isinstance(body, dict):
        return None
    tools = body.get("tools")
    if not isinstance(tools, list):
        return None
    for i, tool in enumerate(tools):
        if not isinstance(tool, dict):
            continue
        name = tool.get("name")
        if tool.get("type") == "custom" and name != "apply_patch":
            return RESPONSES_CUSTOM_TOOL_ERR.format(name=name)
        if tool.get("type") != "namespace":
            continue
        nested = tool.get("tools")
        if not isinstance(nested, list):
            continue
        for j, inner in enumerate(nested):
            if not isinstance(inner, dict):
                continue
            if inner.get("type") == "custom" and inner.get("name") != "apply_patch":
                return RESPONSES_NESTED_CUSTOM_ERR.format(name=inner.get("name"), i=i, j=j)
    return None


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass  # 安静

    # ---------- helpers ----------
    def _json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_body(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        try:
            return json.loads(raw or b"{}")
        except Exception:
            return {"__unparsed__": raw.decode("utf8", "replace")}

    def _record(self, body):
        rec = {
            "path": self.path,
            "method": self.command,
            "headers": {k.lower(): v for k, v in self.headers.items()},
            "body": body,
            "at": time.time(),
        }
        RECORDED.append(rec)
        auth = rec["headers"].get("authorization") or rec["headers"].get("x-api-key") or ""
        print(
            f"[upstream] {self.command} {self.path}  model={body.get('model')!r}  "
            f"key={auth[:14]}…  stream={body.get('stream')}",
            flush=True,
        )
        return rec

    # ---------- routes ----------
    def do_GET(self):
        u = urlparse(self.path)
        if u.path == "/__mock/requests":
            return self._json(200, RECORDED)
        if u.path == "/__mock/fail":
            q = parse_qs(u.query)
            NEXT_FAIL["code"] = int(q.get("code", ["500"])[0])
            return self._json(200, {"ok": True, "next_fail": NEXT_FAIL["code"]})
        if u.path == "/v1/models":
            return self._json(200, {"object": "list", "data": []})
        return self._json(404, {"error": "no such path"})

    def do_POST(self):
        u = urlparse(self.path)
        if u.path == "/__mock/reset":
            RECORDED.clear()
            NEXT_FAIL["code"] = None
            return self._json(200, {"ok": True})

        body = self._read_body()
        self._record(body)

        if NEXT_FAIL["code"]:
            code, NEXT_FAIL["code"] = NEXT_FAIL["code"], None
            return self._json(code, {"error": {"message": f"mock forced {code}"}})

        # anthropic 方言的私有端点：原生 anthropic 上游有、聚合器不一定有
        # （api.rvcompute.com 实测 404）。newgate 按上游能力决定转发拿真值
        # 还是本地粗估——mock 实现它，让 e2e 验证转发路径。
        if u.path.endswith("/count_tokens"):
            return self._json(200, {"input_tokens": 42})

        if strict_reasoning_violation(body):
            print(f"[upstream] STRICT 400: {u.path}", flush=True)
            return self._json(400, STRICT_REASONING_ERR)

        model = body.get("model", "unknown")
        anthropic = u.path.endswith("/messages")
        responses = u.path.endswith("/responses")

        if responses:
            # Responses 方言（codex）。custom 工具那两句 400 按真实判据拦——
            # 那份判据见 responses_custom_violation 上面的实测记录。
            message = responses_custom_violation(body)
            if message:
                print(f"[upstream] CUSTOM-TOOL 400: {u.path}", flush=True)
                return self._json(400, {"error": {"message": message,
                                                  "type": "invalid_request_error",
                                                  "code": "invalid_request_error"}})
            if body.get("stream"):
                return self._responses_stream(model, body, slow=bool(body.get("mock_slow")))
            return self._json(200, self._responses_body(model, body))

        if body.get("stream"):
            # mock_slow：把块间隔拉长——e2e 用它让一条流跨过 restart 窗口，
            # 验证优雅交接不掐在途请求
            return self._stream(model, anthropic,
                                slow=bool(body.get("mock_slow")),
                                tools=bool(body.get("tools")))
        if anthropic:
            # 思考内容 + tool_use：让 newgate 的 thinkcache 有东西可记
            # （tool_use 只在请求带了 tools 时给，id 固定，方便 e2e 断言回填）。
            content = [{"type": "thinking",
                        "thinking": "MOCK-THINKING-ORIGINAL",
                        "signature": "mock-sig"}]
            if body.get("tools"):
                content.append({"type": "tool_use", "id": "toolu_mock_1",
                                "name": "Read", "input": {"path": "x"}})
            content.append({"type": "text", "text": f"MOCK-OK model={model}"})
            return self._json(200, {
                "id": "msg_mock", "type": "message", "role": "assistant",
                "model": model,
                "content": content,
                "stop_reason": "end_turn",
                "usage": {"input_tokens": 7, "output_tokens": 5},
            })
        return self._json(200, {
            "id": "chatcmpl-mock", "object": "chat.completion", "model": model,
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant",
                                     "content": f"MOCK-OK model={model}"}}],
            "usage": {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
        })

    def _stream(self, model, anthropic, slow=False, tools=False):
        """逐块吐，每块之间留间隔——用来验证代理没有缓冲整个响应。

        Anthropic 方言按 Claude Code 的真实形态吐块：thinking 块在前、
        tools 请求加 tool_use 块——thinkcache 观察者靠这两个块的相邻关系
        把推理内容和 tool id 关联起来（e2e 第 8 章的回填断言依赖它）。
        """
        delay = 0.5 if slow else 0.05
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

        def send(ev):
            self.wfile.write(f"data: {json.dumps(ev)}\n\n".encode())
            self.wfile.flush()

        if anthropic:
            send({"type": "message_start",
                  "message": {"id": "msg_mock", "model": model, "content": []}})
            send({"type": "content_block_start", "index": 0,
                  "content_block": {"type": "thinking", "thinking": ""}})
            send({"type": "content_block_delta", "index": 0,
                  "delta": {"type": "thinking_delta",
                            "thinking": "MOCK-THINKING-ORIGINAL"}})
            i = 1
            if tools:
                send({"type": "content_block_start", "index": i,
                      "content_block": {"type": "tool_use", "id": "toolu_mock_1",
                                        "name": "Read", "input": {}}})
                i += 1
            send({"type": "content_block_start", "index": i,
                  "content_block": {"type": "text", "text": ""}})
            for w in ["MOCK", "-", "STREAM", f" {model}"]:
                send({"type": "content_block_delta", "index": i,
                      "delta": {"type": "text_delta", "text": w}})
                time.sleep(delay)
            send({"type": "message_stop"})
        else:
            for w in ["MOCK", "-", "STREAM", f" {model}"]:
                send({"object": "chat.completion.chunk", "model": model,
                      "choices": [{"index": 0, "delta": {"content": w}}]})
                time.sleep(delay)
            send({"object": "chat.completion.chunk", "model": model,
                  "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    # ---------- Responses 方言（codex 走的那条） ----------
    #
    # 逐字节复刻的是**实测到的形状**（2026-09-21，打真实 smt-deepseek/deepseek-flash，
    # 两种工具各抓了一次全流）：
    #
    #   * 每条事件都是 `event: <type>` + `data: {...}` 两行——load-bearing：出站改写
    #     按 `data:` 行定位载荷，事件名那行必须原样留着；
    #   * 一个 reasoning item 在前，工具调用从 output_index 1 起（不是 0！出站改写
    #     按 output_index 记账，这条顺序正是那个键存在的理由）；
    #   * 参数是**一串 delta** 吐出来的，收尾那条事件带完整原文；
    #   * **没有 `data: [DONE]`**——流以 response.completed 结束（这与另外两种方言
    #     不同，也是出站改写器必须处理「最后一个事件不带结尾空行」的现场来源）。
    #
    # 只发 e2e 依赖的那些事件（真实上游还夹着 content_part.* / reasoning_text.done
    # 这类纯展示事件，出站改写对它们一律返回 nil）。少发的那部分不改变任何判据，
    # 但**上面五条必须对**——它们每一条都对应改写器里一处实现细节。

    def _responses_calls(self, body):
        """按请求里的工具树造 function_call 列表（名字, 参数原文）。

        真实上游按模型意图挑；这里**每个 function 工具都调一次**（模型本来就能并行
        调用），理由与 anthropic 那侧「带 tools 就给一个 tool_use」相同：让下游的
        断言有东西可咬。**namespace 里的也照调**——实测过（2026-09-21，真实
        deepseek-flash）模型真的会调用 namespace 里的函数，事件里报的是**裸名字**
        （`name: "spawn_agent"`，不带 namespace 前缀）。

        参数按工具自己声明的**第一个**属性名造。降级出来的那条工具只有 `input`
        （见 lift.go 的 freeformParams），原生工具是它自己的（`cmd` 之类）——于是
        同一条流上同时出现「客户端该收到 custom_tool_call 的」与「一个字节都不许
        动的」两种调用，这正是 e2e 要分开验的那件事。
        """
        calls = []
        for name, params in self._responses_functions(body.get("tools")):
            props = params.get("properties") if isinstance(params, dict) else None
            if isinstance(props, dict) and props:
                first = next(iter(props))
                args = json.dumps({first: f"MOCK-TOOL-{name}"})
            else:
                args = "{}"
            calls.append((name, args))
        return calls

    def _responses_functions(self, tools):
        """工具树里的 function 叶子，深度优先（namespace 递归进去）。"""
        found = []
        if not isinstance(tools, list):
            return found
        for tool in tools:
            if not isinstance(tool, dict):
                continue
            if tool.get("type") == "function":
                found.append((tool.get("name") or "tool", tool.get("parameters")))
            elif tool.get("type") == "namespace":
                found.extend(self._responses_functions(tool.get("tools")))
        return found

    def _responses_body(self, model, body):
        """非流式：一个 response 对象，形状与 response.completed 里那个一致。"""
        output = [self._responses_reasoning_item()]
        for i, (name, args) in enumerate(self._responses_calls(body), start=1):
            output.append({"type": "function_call", "id": f"item_mock_{i}",
                           "status": "completed", "name": name,
                           "call_id": f"call_mock_{i}", "arguments": args})
        return {"id": "resp_mock", "object": "response", "created_at": 0,
                "completed_at": 0, "model": model, "status": "completed",
                "output": output, "error": None,
                "usage": {"input_tokens": 7, "output_tokens": 5, "total_tokens": 12}}

    @staticmethod
    def _responses_reasoning_item():
        return {"type": "reasoning", "id": "rs_mock", "status": "completed",
                "summary": [], "encrypted_content": "mock-encrypted",
                "content": [{"type": "reasoning_text",
                             "text": "MOCK-REASONING-ORIGINAL"}]}

    def _responses_stream(self, model, body, slow=False):
        delay = 0.5 if slow else 0.05
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

        def send(ev):
            # 事件名 = 载荷的 type（真实上游每一条都是这样）。**两行都要发**：
            # 出站改写定位 data 行，但事件名那行是客户端分派的依据。
            self.wfile.write(
                f"event: {ev['type']}\ndata: {json.dumps(ev)}\n\n".encode())
            self.wfile.flush()

        reasoning = self._responses_reasoning_item()
        send({"type": "response.created",
              "response": {"id": "resp_mock", "object": "response", "status": "in_progress",
                           "model": model, "output": []}})

        # reasoning item 占 output_index 0——工具调用因此从 1 起。
        item = dict(reasoning)
        item["status"] = "in_progress"
        item.pop("content", None)
        send({"type": "response.output_item.added", "output_index": 0, "item": item})
        for w in ["MOCK", "-REASONING"]:
            send({"type": "response.reasoning_text.delta", "output_index": 0,
                  "item_id": reasoning["id"], "content_index": 0, "delta": w})
            time.sleep(delay)
        send({"type": "response.output_item.done", "output_index": 0, "item": reasoning})

        calls = self._responses_calls(body)
        for i, (name, args) in enumerate(calls, start=1):
            item_id = f"item_mock_{i}"
            call_id = f"call_mock_{i}"
            send({"type": "response.output_item.added", "output_index": i,
                  "item": {"type": "function_call", "id": item_id,
                           "status": "in_progress", "name": name,
                           "call_id": call_id, "arguments": ""}})
            # 参数拆成几段 delta 吐——这是真实形态，也是出站改写必须**攒**参数的
            # 原因（收尾那条单独看也可能为空，见 st-tools.go 的 degradedCall）。
            for at in range(0, len(args), 4):
                send({"type": "response.function_call_arguments.delta",
                      "output_index": i, "item_id": item_id, "delta": args[at:at + 4]})
                time.sleep(delay)
            send({"type": "response.function_call_arguments.done",
                  "output_index": i, "item_id": item_id, "arguments": args})
            send({"type": "response.output_item.done", "output_index": i,
                  "item": {"type": "function_call", "id": item_id,
                           "status": "completed", "name": name,
                           "call_id": call_id, "arguments": args}})

        completed = self._responses_body(model, body)
        send({"type": "response.completed", "response": completed})


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=18080)
    a = ap.parse_args()
    srv = ThreadingHTTPServer(("127.0.0.1", a.port), Handler)
    print(f"[upstream] listening 127.0.0.1:{a.port}", flush=True)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    sys.exit(main())
