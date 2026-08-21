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

        model = body.get("model", "unknown")
        anthropic = u.path.endswith("/messages")

        if body.get("stream"):
            return self._stream(model, anthropic)
        if anthropic:
            return self._json(200, {
                "id": "msg_mock", "type": "message", "role": "assistant",
                "model": model,
                "content": [{"type": "text", "text": f"MOCK-OK model={model}"}],
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

    def _stream(self, model, anthropic):
        """逐块吐，每块之间留间隔——用来验证代理没有缓冲整个响应。"""
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
            for w in ["MOCK", "-", "STREAM", f" {model}"]:
                send({"type": "content_block_delta",
                      "delta": {"type": "text_delta", "text": w}})
                time.sleep(0.05)
            send({"type": "message_stop"})
        else:
            for w in ["MOCK", "-", "STREAM", f" {model}"]:
                send({"object": "chat.completion.chunk", "model": model,
                      "choices": [{"index": 0, "delta": {"content": w}}]})
                time.sleep(0.05)
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
