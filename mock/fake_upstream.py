#!/usr/bin/env python3
"""Fake upstream: impersonates the your-gateway gateway, zero tokens.

Records everything it receives, so we can assert what model newgate actually rewrote it to and

  python3 mock/fake_upstream.py --port 18080 [--log requests.jsonl]

Routes:
  POST /v1/chat/completions   openai dialect (streaming supported)
  POST /v1/messages           anthropic dialect (streaming supported)
  POST /v1/responses          responses dialect (the one codex uses; streaming supported)
  GET  /__mock/requests       returns every request received (JSON)
  POST /__mock/reset          clears the records
  GET  /__mock/fail?code=429  makes the next request return that status code
"""
import argparse, json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

RECORDED = []
NEXT_FAIL = {"code": None}

# The real criterion for that upstream 400. **Rewritten from measurement on 2026-09-18**: this
# used to copy the official docs, which say a history with tools and thinking on must pass
# non-empty reasoning back on every assistant turn. That does not hold on the real upstream --
# tested cell by cell against the real smt-deepseek/deepseek-flash (3/3 per cell;
#
#   reasoning_content's shape (real text / omitted field / empty string / placeholder)
#   **makes no difference at all**; the only thing that matters is the **tail shape**:
#     the last user message's content[] is all tool_result blocks  -> 400
#     the same content[] has one extra non-empty text block (even a single space) -> 200
#
# The error text is still the upstream's: it reports "no new instruction this turn" as
# "reasoning was not passed back". That mismatch between text and real cause is the nastiest
# thing about this upstream, and why this check must go by shape, not by text.
#
# Two more measured behaviors this function **deliberately does not simulate**: an empty text
# block gives `missing field text`; ending the array with assistant is another rule. The plugin fixes neither family, so neither is blocked.
STRICT_REASONING_ERR = {
    "type": "error",
    "error": {
        "type": "invalid_request_error",
        "message": "The `reasoning_content` in the thinking mode must be "
                   "passed back to the API. (request id: mock-e2e-strict)",
    },
}


def strict_reasoning_violation(body):
    """True on violation. The criterion is the **tail shape**, not the reasoning field -- see the measurement notes above."""
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
        return False  # content is a string = there is text already, so let it through
    return all(isinstance(b, dict) and b.get("type") == "tool_result"
               for b in blocks)


# The Responses dialect's (the one codex uses) two 400s for custom tools. **Written from
# measurement on 2026-09-21** against the real smt-deepseek/deepseek-flash, trying custom tools in the tool tree:
#
#   at top level        -> Unsupported custom tool: 'exec'. Only 'apply_patch' is supported.
#   inside a namespace  -> Currently custom tools are not allowed inside a namespace.
#                      Found custom tool 'exec' in tools[1].tools[0].
#
# Both are **request-shape** problems (apply_patch is the only custom this vendor accepts), so
# block them faithfully: a lenient fake upstream would make this e2e a green fake test.
#
# The two **envelopes** are not consistent on the real upstream (different backend nodes of the
# same vendor gave two codes and wordings), so only the **sentence** is verbatim -- it is the criterion, not the envelope.
#
# One cell is inferred from its family, not measured: apply_patch inside a namespace. This
# function lets it through (consistent with top level). That cell is unreachable on the product
# side (degradation rewrites every non-apply_patch custom, and it should never sit in a namespace); kept only so "uncertain" is not written as certainty.
RESPONSES_CUSTOM_TOOL_ERR = "Unsupported custom tool: '{name}'. Only 'apply_patch' is supported."
RESPONSES_NESTED_CUSTOM_ERR = ("Currently custom tools are not allowed inside a "
                               "namespace. Found custom tool '{name}' in tools[{i}].tools[{j}].")


def responses_custom_violation(body):
    """Returns the text of that 400 on violation, None otherwise. Criterion = the real upstream's two sentences (see above)."""
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


# The `reasoning.effort` allowlist for OpenAI Responses: this vendor **always thinks** and
#
# Field notes (2026-09-22, against the real smt-glm/glm-5.3, `POST /v1/responses` cell by cell):
#
#   effort = low | high | max      → 200
#   effort = medium | minimal | none -> 400 (verbatim upstream text: "该模型始终思考，不支持关闭思考；
#                                      请使用 low、high 或 max。")
#   reasoning field absent / {} / {"effort": null} / {"summary":"auto"} -> 200
#
# Two more measured shapes (the fake upstream **must** follow them, or the e2e pins the wrong
# shape): nested wins: `reasoning.effort` overrides the top-level `effort`, so reading both at
# top level is wrong; and only with `stream=true` does this 400 **skip the HTTP status code** --
# it opens the stream with 200, then stuffs the failure into it (`response.failed`); what the
# client sees verbatim is "stream disconnected before completion", the user's exact words.
#
# The error text is kept verbatim: it is one of the **match criteria** for that newgate patch,
# and the evidence that "the upstream really does reject it this way". Rewording it swaps the criterion out.
RESPONSES_REASONING_EFFORT_ERR = (
    "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。")
RESPONSES_ACCEPTED_EFFORTS = ("low", "high", "max")


def responses_effort_violation(body):
    """Would this reasoning.effort get a 400 from the upstream? True if so.

    Only the **nested** level (`reasoning.effort`) is read -- the top-level `effort` is not where
    this vendor looks, and treating it as the criterion would invent a 400 the real upstream does not have.
    """
    if not isinstance(body, dict):
        return False
    reasoning = body.get("reasoning")
    if not isinstance(reasoning, dict):
        return False
    effort = reasoning.get("effort")
    if effort is None:  # field present but value null: measured 200 (the request not to think was dropped, but no error)
        return False
    return effort not in RESPONSES_ACCEPTED_EFFORTS


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass  # quiet

    # ---------- helpers ----------
    def _json(self, code, obj, ensure_ascii=True):
        body = json.dumps(obj, ensure_ascii=ensure_ascii).encode()
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

        # A private endpoint of the anthropic dialect: native anthropic upstreams have it, aggregators
        # may not (api.rvcompute.com measured 404). newgate decides by upstream capability whether to
        # forward for the real value or estimate locally -- the mock implements it so the e2e checks that path.
        if u.path.endswith("/count_tokens"):
            return self._json(200, {"input_tokens": 42})

        if strict_reasoning_violation(body):
            print(f"[upstream] STRICT 400: {u.path}", flush=True)
            return self._json(400, STRICT_REASONING_ERR)

        model = body.get("model", "unknown")
        anthropic = u.path.endswith("/messages")
        responses = u.path.endswith("/responses")

        if responses:
            # The Responses dialect (codex). The two custom-tool 400s are blocked by the real
            # criterion -- see the measurement notes above responses_custom_violation.
            message = responses_custom_violation(body)
            if message:
                print(f"[upstream] CUSTOM-TOOL 400: {u.path}", flush=True)
                return self._json(400, {"error": {"message": message,
                                                  "type": "invalid_request_error",
                                                  "code": "invalid_request_error"}})
            if responses_effort_violation(body):
                print(f"[upstream] EFFORT 400: {u.path}", flush=True)
                # The envelope is reproduced from measurement: nodes of the same upstream gave two kinds. One `code`
                # is "invalid_request_error" with a single sentence; the other `code` is "400001", the
                # sentence wrapped in "The request is invalid: .... Please check ...". **The criterion
                # inside the sentence is unchanged** -- newgate's signature match runs `bytes.Contains` on
                # that sentence; changing the envelope does not make the e2e green (the criterion is
                # not in the envelope). Field notes: dump/err-400-req000870.meta.txt (route normal -> smt-glm/glm-5.3, codex_cli_rs/0.155.1).
                # smt-glm/glm-5.3，codex_cli_rs/0.155.1）。
                err = {"error": {"message": RESPONSES_REASONING_EFFORT_ERR,
                                 "type": "invalid_request_error",
                                 "param": "", "code": "400001"}}
                if body.get("stream"):
                    # The streaming branch **opens the stream with 200 first**, then stuffs the
                    # failure into the event stream -- that is exactly what the real upstream does,
                    # and the client's "stream disconnected before completion" comes from here;
                    return self._responses_failed(model, body, err["error"]["message"])
                return self._json(400, err, ensure_ascii=False)
            if body.get("stream"):
                return self._responses_stream(model, body, slow=bool(body.get("mock_slow")))
            return self._json(200, self._responses_body(model, body))

        if body.get("stream"):
            # mock_slow: stretch the gap between chunks -- the e2e uses it to carry a stream
            # across the restart window, verifying graceful handover does not cut in-flight requests
            return self._stream(model, anthropic,
                                slow=bool(body.get("mock_slow")),
                                tools=bool(body.get("tools")))
        if anthropic:
            # Thinking content + tool_use: so newgate's thinkcache has something to record
            # (tool_use is only given when the request carries tools, with a fixed id, so the e2e can assert the backfill).
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
        """Emits chunk by chunk with a gap in between -- used to verify the proxy does not buffer the whole response.

        The Anthropic dialect emits chunks in Claude Code's real shape: thinking blocks first,
        and tool-carrying requests add a tool_use block -- the thinkcache observer uses the
        adjacency of these two blocks to associate the reasoning with the tool id (the e2e's chapter 8 backfill assertion depends on it).
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

    # ---------- The Responses dialect (the one codex uses) ----------
    #
    # What is reproduced byte for byte is the **measured shape** (2026-09-21, against the real
    # smt-deepseek/deepseek-flash, capturing the full stream once for each of two tool kinds):
    #
    #   * every event is two lines, `event: <type>` + `data: {...}` -- load-bearing: the outbound
    #     rewrite locates the payload by the `data:` line, so the event-name line must stay verbatim;
    #   * one reasoning item comes first, so tool calls start at output_index 1 (not 0! the outbound
    #     rewrite accounts by output_index, and this ordering is exactly why that key exists);
    #   * arguments come out as **a series of deltas**, and the closing event carries the full text;
    #   * **there is no `data: [DONE]`** -- the stream ends with response.completed (this differs
    #     from the other two dialects, and is why the rewriter must handle "the last event has no trailing blank line").
    #
    # Only the events the e2e depends on are sent (the real upstream also interleaves purely
    # presentational events like content_part.* / reasoning_text.done, which the outbound rewrite
    # always returns nil for). Omitting them changes no criterion, but **the five points above must be right** -- each maps to one rewriter detail.

    def _responses_calls(self, body):
        """Builds the function_call list from the request's tool tree (name, raw arguments).

        The real upstream picks by model intent; here **every function tool is called once**
        (models can call in parallel anyway), for the same reason as on the anthropic side:
        "give one tool_use when tools are present" -- so downstream assertions have something
        to bite on. **Namespaced functions are called too** -- measured (2026-09-21, real
        deepseek-flash): it really does call them, reporting the **bare name** (`name: "spawn_agent"`, no prefix).

        Arguments are built from the **first** property the tool itself declares. A degraded tool has only `input`
        (see freeformParams in lift.go); a native tool has its own (`cmd` and the like) -- so one
        stream carries both "the call the client should get as custom_tool_call" and "the one no
        byte of which may change"; the e2e checks those separately.
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
        """The function leaves of the tool tree, depth first (namespaces are recursed into)."""
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
        """Non-streaming: a single response object, shaped like the one inside response.completed."""
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

    def _responses_failed(self, model, body, message):
        """A **streaming** rejection: open with 200 first, then stuff the failure into the event stream.

        This is the nastiest shape on the responses path, and the reason this e2e cannot do
        without it: a probe or patch written against the HTTP status code **never sees** this
        failure on the streaming path (the status is 200 and the connection is established
        normally); the client just sees "stream disconnected before completion" -- what the user reported on 2026-09-22.
        """
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

        def send(ev):
            self.wfile.write(
                f"event: {ev['type']}\ndata: {json.dumps(ev)}\n\n".encode())
            self.wfile.flush()

        send({"type": "response.created",
              "response": {"id": "resp_mock", "object": "response",
                           "status": "in_progress", "model": model, "output": []}})
        send({"type": "response.failed",
              "response": {"id": "resp_mock", "object": "response",
                           "status": "failed", "model": model, "output": [],
                           "error": {"code": "invalid_request_error",
                                     "message": message}}})

    def _responses_stream(self, model, body, slow=False):
        delay = 0.5 if slow else 0.05
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

        def send(ev):
            # Event name = the payload's type (so it is on every real upstream event). **Both lines
            # must be sent**: the rewriter locates the data line, but the event name is what the client dispatches by.
            self.wfile.write(
                f"event: {ev['type']}\ndata: {json.dumps(ev)}\n\n".encode())
            self.wfile.flush()

        reasoning = self._responses_reasoning_item()
        send({"type": "response.created",
              "response": {"id": "resp_mock", "object": "response", "status": "in_progress",
                           "model": model, "output": []}})

        # The reasoning item takes output_index 0 -- tool calls therefore start at 1.
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
            # Arguments are emitted as several deltas -- the real shape, and why the outbound
            # rewrite must **accumulate** arguments (the closing one can look empty alone; see degradedCall in st-tools.go).
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
