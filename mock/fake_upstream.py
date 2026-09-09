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

# 模拟 DeepSeek 官方思考模式最严口径的检查：请求带了 tools 且思考开着时，
# 每条 assistant 消息都必须回传**非空**的推理内容（reasoning_content 字段，
# 或 Anthropic 方言 content[] 里 thinking 块的文本）。空串 = 没回传，照样 400
# —— 2026-09 在 new-api 直连官方的部署上实抓到的行为，本仓库
# gateway/special/st-deepseek.go 的占位符修法就是冲它去的。
STRICT_REASONING_ERR = {
    "type": "error",
    "error": {
        "type": "invalid_request_error",
        "message": "The `reasoning_content` in the thinking mode must be "
                   "passed back to the API. (request id: mock-e2e-strict)",
    },
}


def strict_reasoning_violation(body):
    """有违规返回 True。只对「带 tools 且思考开着」的请求生效（官方文档口径）。"""
    if not isinstance(body, dict):
        return False
    if not body.get("tools"):
        return False
    thinking = body.get("thinking")
    if isinstance(thinking, dict) and thinking.get("type") == "disabled":
        return False
    for m in body.get("messages") or []:
        if not isinstance(m, dict) or m.get("role") != "assistant":
            continue
        rc = m.get("reasoning_content")
        if isinstance(rc, str) and rc.strip():
            continue
        content = m.get("content")
        if isinstance(content, list):
            text = "".join(
                b.get("thinking", "") for b in content
                if isinstance(b, dict) and b.get("type") == "thinking"
            )
            if text.strip():
                continue
        return True
    return False


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
