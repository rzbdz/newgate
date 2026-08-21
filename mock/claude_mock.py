#!/usr/bin/env python3
"""假 Claude Code —— 按真实 Claude Code 的方式解析档位，打印 curl 并真的发请求。

不用启动 claude 就能验证 newgate 的注入是否生效。

Claude Code 的模型解析是两层的：
    settings.json  "model": "opus[1m]"      ← 选哪个档位
    ANTHROPIC_DEFAULT_OPUS_MODEL=heavy      ← 那个档位指向什么

所以本脚本先看 env（shim 注入的），再看 settings.json 的 env 块，
最后回落到 settings.json 的 model 字段——和 Claude Code 的优先级一致。

常用:
  claude-mock                      # 用 settings.json 里的 model 档位
  claude-mock --tier opus          # 指定档位
  claude-mock --tier haiku --stream
  claude-mock --subagent           # 走 CLAUDE_CODE_SUBAGENT_MODEL
  claude-mock --all-tiers          # 四个档位全打一遍
  claude-mock --dry                # 只打印 curl
  claude-mock --show               # 只显示解析结果，不发请求
"""
import argparse, json, os, shlex, sys, urllib.parse, urllib.request, urllib.error

TIER_ENV = {
    "opus":   "ANTHROPIC_DEFAULT_OPUS_MODEL",
    "sonnet": "ANTHROPIC_DEFAULT_SONNET_MODEL",
    "haiku":  "ANTHROPIC_DEFAULT_HAIKU_MODEL",
    "fable":  "ANTHROPIC_DEFAULT_FABLE_MODEL",
}
SUBAGENT_ENV = "CLAUDE_CODE_SUBAGENT_MODEL"
DEFAULT_SETTINGS = os.path.expanduser("~/.claude/settings.json")


def opener_for(url):
    """loopback 强制绕过出站代理。

    坑：urllib 默认读 http_proxy/https_proxy。集群机器上配了出站代理时，
    发往 127.0.0.1 的请求会被送去代理，代理连不上本机 loopback 就回
    502 Bad Gateway——而 newgate 日志里一条请求都没有。
    """
    host = urllib.parse.urlparse(url).hostname or ""
    if host in ("127.0.0.1", "localhost", "::1", "0.0.0.0"):
        return urllib.request.build_opener(urllib.request.ProxyHandler({}))
    return urllib.request.build_opener()


def load_settings(path):
    if not os.path.exists(path):
        return {}
    try:
        with open(path) as f:
            return json.load(f)
    except Exception as e:
        raise SystemExit(f"{path} 解析失败: {e}")


def resolve(key, settings):
    """按 Claude Code 的优先级取一个变量：进程 env > settings.json 的 env 块。"""
    if os.environ.get(key):
        return os.environ[key], "shell env"
    blk = settings.get("env") or {}
    if blk.get(key):
        return blk[key], "settings.json env"
    return None, None


def strip_1m(s):
    """[1m] 是 Claude Code 声明 100 万上下文的客户端标记，不属于模型 id。"""
    t = s.strip()
    return t[:-4].strip() if t.lower().endswith("[1m]") else t


def pick_tier(settings, want):
    """决定用哪个档位。want 为空时读 settings.json 的 model 字段。"""
    if want:
        return want, "--tier"
    m = settings.get("model")
    if not m:
        return "sonnet", "默认（settings.json 没有 model 字段）"
    base = strip_1m(m)
    # "opusplan" 在 Plan Mode 下用 opus，否则用 sonnet
    if base == "opusplan":
        return "sonnet", f'settings.json model={m!r}（opusplan，Plan Mode 外用 sonnet）'
    if base in TIER_ENV:
        return base, f"settings.json model={m!r}"
    # 直接写了具体模型名
    return None, f"settings.json model={m!r}（不是档位别名，直接当模型名用）"


def curl_cmd(url, token, payload):
    host = urllib.parse.urlparse(url).hostname or ""
    pre = ["curl", "-sS", "-i"]
    if host in ("127.0.0.1", "localhost", "::1"):
        pre += ["--noproxy", "*"]
    parts = pre + ["-X", "POST", url,
                   "-H", "Content-Type: application/json",
                   "-H", f"x-api-key: {token}",
                   "-H", "anthropic-version: 2023-06-01",
                   "-d", json.dumps(payload, ensure_ascii=False)]
    return " ".join(shlex.quote(p) for p in parts)


def call(url, token, payload, timeout):
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("x-api-key", token)
    req.add_header("anthropic-version", "2023-06-01")
    try:
        with opener_for(url).open(req, timeout=timeout) as r:
            for h in ("X-Newgate-Route", "X-Newgate-Profile",
                      "X-Newgate-Chain", "X-Newgate-Failover"):
                if r.headers.get(h):
                    print(f"  \033[33m{h}: {r.headers[h]}\033[0m")
            if payload.get("stream"):
                n = 0
                for raw in r:
                    line = raw.decode("utf8", "replace").rstrip()
                    if line:
                        n += 1
                        if n <= 2:
                            print(f"  {line[:120]}")
                print(f"  \033[32m✓ {r.status}, {n} 个 SSE 块\033[0m")
            else:
                print(f"  \033[32m✓ {r.status}\033[0m "
                      f"{r.read()[:240].decode('utf8','replace')}")
            return True
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf8", "replace")
        for h in ("X-Newgate-Route", "X-Newgate-Chain", "X-Newgate-Evidence"):
            if e.headers.get(h):
                print(f"  \033[33m{h}: {e.headers[h]}\033[0m")
        print(f"  \033[31m✗ HTTP {e.code}\033[0m {body[:400]}")
        return False
    except Exception as e:
        print(f"  \033[31m✗ {type(e).__name__}: {e}\033[0m")
        return False


def main():
    ap = argparse.ArgumentParser(
        formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    ap.add_argument("--settings", default=DEFAULT_SETTINGS)
    ap.add_argument("--tier", choices=sorted(TIER_ENV))
    ap.add_argument("--subagent", action="store_true",
                    help=f"用 {SUBAGENT_ENV}")
    ap.add_argument("--all-tiers", action="store_true")
    ap.add_argument("--stream", action="store_true")
    ap.add_argument("--dry", action="store_true", help="只打印 curl")
    ap.add_argument("--show", action="store_true", help="只显示解析，不发请求")
    ap.add_argument("--prompt", default="ping")
    ap.add_argument("--timeout", type=float, default=180)
    a = ap.parse_args()

    st = load_settings(a.settings)
    base, base_src = resolve("ANTHROPIC_BASE_URL", st)
    token, tok_src = resolve("ANTHROPIC_AUTH_TOKEN", st)
    if not token:
        token, tok_src = resolve("ANTHROPIC_API_KEY", st)

    print(f"settings   {a.settings}")
    print(f"  model 字段  {st.get('model')!r}")
    print(f"  env 块      {'有' if st.get('env') else '无'}")
    print(f"baseURL    {base}   \033[2m({base_src})\033[0m")
    print(f"token      {(token or '')[:12]}…   \033[2m({tok_src})\033[0m")
    if not base:
        raise SystemExit("\n没有 ANTHROPIC_BASE_URL。跑 `newgate shim install` 并重开 shell。")

    # 决定要测哪些档位
    jobs = []
    if a.subagent:
        v = os.environ.get(SUBAGENT_ENV) or (st.get("env") or {}).get(SUBAGENT_ENV)
        if not v:
            raise SystemExit(f"{SUBAGENT_ENV} 没设")
        jobs.append(("subagent", v))
    elif a.all_tiers:
        for tier, envk in sorted(TIER_ENV.items()):
            v, _ = resolve(envk, st)
            jobs.append((tier, v))
    else:
        tier, why = pick_tier(st, a.tier)
        print(f"档位       {tier or '(直接模型名)'}   \033[2m({why})\033[0m")
        if tier:
            v, src = resolve(TIER_ENV[tier], st)
            if not v:
                raise SystemExit(
                    f"\n{TIER_ENV[tier]} 没设——newgate 的注入没生效。\n"
                    f"  检查：newgate shim status / which claude")
            print(f"  {TIER_ENV[tier]} = {v}   \033[2m({src})\033[0m")
            jobs.append((tier, v))
        else:
            jobs.append(("literal", strip_1m(st.get("model", ""))))

    url = base.rstrip("/") + "/v1/messages"
    ok = fail = 0
    for label, model in jobs:
        if not model:
            print(f"\n[{label}] \033[31m未设置对应的 env\033[0m")
            fail += 1
            continue
        payload = {"model": model, "max_tokens": 32,
                   "messages": [{"role": "user", "content": a.prompt}]}
        if a.stream:
            payload["stream"] = True
        print(f"\n\033[1m[{label}]\033[0m 发出的 model = \033[36m{model}\033[0m")
        print(f"  endpoint  {url}")
        print(f"  curl:\n    {curl_cmd(url, token, payload)}")
        if a.dry or a.show:
            continue
        if call(url, token, payload, a.timeout):
            ok += 1
        else:
            fail += 1

    if not (a.dry or a.show):
        print(f"\n结果: \033[32m{ok} 成功\033[0m, \033[31m{fail} 失败\033[0m")
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
