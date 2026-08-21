#!/usr/bin/env python3
"""假 opencode —— 按真实 opencode 的方式解析配置，打印 curl 并真的发请求。

不用装 opencode 就能调 newgate。

常用:
  # 列出所有可用 level（agent + category），带当前解析目标
  opencode-mock --list

  # 按 agent 名跑（支持中文别名）
  opencode-mock --level sisyphus
  opencode-mock --level 西西弗斯
  opencode-mock --level ultrabrain --stream

  # 只打印 curl，不发请求
  opencode-mock --level oracle --dry

  # 顶层槽位 / 直接指定
  opencode-mock --slot small_model
  opencode-mock --model newgate/light

  # 全部 level 扫一遍
  opencode-mock --all

配置目录默认 ~/.config/opencode，用 --config-dir 改。
解析规则照抄 opencode：model 形如 "<provider>/<model>"，
去 provider.<provider>.options 取 baseURL / apiKey，按 npm 判断方言。
"""
import argparse, json, os, shlex, sys, urllib.parse, urllib.request, urllib.error


def opener_for(url):
    """给 loopback 目标建一个绕过所有代理的 opener。

    坑：urllib 默认读 http_proxy/https_proxy 环境变量。集群机器上常配了
    出站代理，于是发往 127.0.0.1 的请求也被送去代理，代理连不上本机
    loopback 就回 502 Bad Gateway——而 newgate 日志里一条请求都没有。
    排查这个能耗掉一小时，所以这里直接强制绕过。
    """
    host = urllib.parse.urlparse(url).hostname or ""
    if host in ("127.0.0.1", "localhost", "::1", "0.0.0.0"):
        return urllib.request.build_opener(urllib.request.ProxyHandler({}))
    return urllib.request.build_opener()

DEFAULT_DIR = os.path.expanduser("~/.config/opencode")

# 中文别名 -> oh-my-openagent 里的 key
ALIASES = {
    "西西弗斯": "sisyphus", "西西弗斯junior": "sisyphus-junior", "小西西弗斯": "sisyphus-junior",
    "赫菲斯托斯": "hephaestus", "神谕": "oracle", "先知": "oracle",
    "图书管理员": "librarian", "探索": "explore", "多模态": "multimodal-looker",
    "普罗米修斯": "prometheus", "墨提斯": "metis", "摩姆斯": "momus", "阿特拉斯": "atlas",
    "超脑": "ultrabrain", "深度": "deep", "快": "quick", "艺术": "artistry",
    "视觉工程": "visual-engineering", "写作": "writing",
}


def load(path):
    with open(path) as f:
        return json.load(f)


def resolve(cfg, ref):
    """'newgate/heavy' -> (baseURL, apiKey, model, dialect, provider_id)"""
    if "/" not in ref:
        raise SystemExit(f"model 引用没有 provider 前缀: {ref!r}")
    pid, model = ref.split("/", 1)
    prov = (cfg.get("provider") or {}).get(pid)
    if not prov:
        have = list(cfg.get("provider") or {})
        raise SystemExit(f"opencode.json 里没有 provider {pid!r}（有：{have}）\n"
                         f"  → 如果期望 'newgate'，先跑 `newgate start` 接管配置")
    opts = prov.get("options") or {}
    base = opts.get("baseURL") or opts.get("baseUrl")
    if not base:
        raise SystemExit(f"provider {pid} 没有 baseURL")
    dialect = "anthropic" if "anthropic" in prov.get("npm", "") else "openai"
    return base.rstrip("/"), opts.get("apiKey", ""), model, dialect, pid


def levels(oa):
    """收集所有 level：{name: {"model":…, "variant":…, "fallbacks":[…], "kind":…}}"""
    out = {}
    for kind in ("agents", "categories"):
        for name, spec in (oa.get(kind) or {}).items():
            if not isinstance(spec, dict):
                continue
            out[name] = {
                "kind": kind[:-1],
                "model": spec.get("model"),
                "variant": spec.get("variant"),
                "fallbacks": [f.get("model") for f in (spec.get("fallback_models") or [])
                              if isinstance(f, dict) and f.get("model")],
            }
    return out


def build(dialect, model, stream, prompt):
    payload = {"model": model, "max_tokens": 64,
               "messages": [{"role": "user", "content": prompt}]}
    if stream:
        payload["stream"] = True
    return ("/messages" if dialect == "anthropic" else "/chat/completions"), payload


def curl_cmd(url, key, dialect, payload):
    hdr = ["-H", "Content-Type: application/json"]
    if dialect == "anthropic":
        hdr += ["-H", f"x-api-key: {key}", "-H", "anthropic-version: 2023-06-01"]
    else:
        hdr += ["-H", f"Authorization: Bearer {key}"]
    host = urllib.parse.urlparse(url).hostname or ""
    pre = ["curl", "-sS", "-i"]
    if host in ("127.0.0.1", "localhost", "::1"):
        pre += ["--noproxy", "*"]   # 别让出站代理劫走 loopback 请求
    parts = pre + ["-X", "POST", url] + hdr + \
            ["-d", json.dumps(payload, ensure_ascii=False)]
    return " ".join(shlex.quote(p) for p in parts)


def show(label, ref, base, key, model, dialect, pid, payload, url):
    print(f"\n\033[1m[{label}]\033[0m  {ref}")
    print(f"  endpoint  {url}")
    print(f"  provider  {pid}  (方言 {dialect})")
    print(f"  model     \033[36m{model}\033[0m")
    print(f"  apiKey    {key[:12]}…" if key else "  apiKey    (空)")
    print(f"  curl:\n    {curl_cmd(url, key, dialect, payload)}")


def call(url, key, dialect, payload, timeout):
    data = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    if dialect == "anthropic":
        req.add_header("x-api-key", key)
        req.add_header("anthropic-version", "2023-06-01")
    else:
        req.add_header("Authorization", f"Bearer {key}")
    try:
        with opener_for(url).open(req, timeout=timeout) as r:
            for h in ("X-Newgate-Route", "X-Newgate-Profile", "X-Newgate-Failover"):
                if r.headers.get(h):
                    print(f"  \033[33m{h}: {r.headers[h]}\033[0m")
            if payload.get("stream"):
                n = 0
                for raw in r:
                    line = raw.decode("utf8", "replace").rstrip()
                    if line:
                        n += 1
                        if n <= 2 or line.endswith("[DONE]"):
                            print(f"  {line[:120]}")
                print(f"  \033[32m✓ {r.status}, {n} 个 SSE 块\033[0m")
            else:
                body = r.read().decode("utf8", "replace")
                print(f"  \033[32m✓ {r.status}\033[0m {body[:260]}")
            return True
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf8", "replace")
        for h in ("X-Newgate-Route", "X-Newgate-Failover"):
            if e.headers.get(h):
                print(f"  \033[33m{h}: {e.headers[h]}\033[0m")
        print(f"  \033[31m✗ HTTP {e.code}\033[0m {body[:400]}")
        return False
    except Exception as e:
        print(f"  \033[31m✗ {type(e).__name__}: {e}\033[0m")
        return False


def main():
    ap = argparse.ArgumentParser(add_help=True,
        formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    ap.add_argument("--config-dir", default=DEFAULT_DIR)
    ap.add_argument("--level", "-L", help="agent / category 名（支持中文别名）")
    ap.add_argument("--slot", help="opencode.json 顶层槽位：model / small_model")
    ap.add_argument("--model", help="直接给 provider/model 引用")
    ap.add_argument("--list", action="store_true", help="列出所有 level")
    ap.add_argument("--all", action="store_true", help="跑所有 level")
    ap.add_argument("--fallbacks", action="store_true", help="连 fallback 链一起跑")
    ap.add_argument("--stream", action="store_true")
    ap.add_argument("--dry", action="store_true", help="只打印 curl，不发请求")
    ap.add_argument("--prompt", default="ping")
    ap.add_argument("--timeout", type=float, default=180)
    a = ap.parse_args()

    oc_path = os.path.join(a.config_dir, "opencode.json")
    if not os.path.exists(oc_path):
        raise SystemExit(f"找不到 {oc_path}（--config-dir 指对了吗）")
    cfg = load(oc_path)

    oa_path = os.path.join(a.config_dir, "oh-my-openagent.json")
    lv = levels(load(oa_path)) if os.path.exists(oa_path) else {}

    if a.list:
        if not lv:
            raise SystemExit(f"找不到 {oa_path}")
        rev = {}
        for cn, en in ALIASES.items():
            rev.setdefault(en, []).append(cn)
        print(f"{'LEVEL':<22} {'KIND':<9} {'MODEL':<26} VARIANT   别名")
        print("─" * 92)
        for name in sorted(lv, key=lambda n: (lv[n]["kind"], n)):
            d = lv[name]
            print(f"{name:<22} {d['kind']:<9} {str(d['model']):<26} "
                  f"{str(d['variant'] or '-'):<9} {'/'.join(rev.get(name, []))}")
        print(f"\n共 {len(lv)} 个 level。用法: opencode-mock --level <名>")
        return 0

    # 决定要跑哪些引用
    refs = []
    if a.level:
        key = ALIASES.get(a.level, a.level)
        if key not in lv:
            near = [n for n in lv if key.lower() in n.lower()]
            raise SystemExit(f"没有 level {a.level!r}"
                             + (f"（相近：{near}）" if near else "")
                             + "\n  → opencode-mock --list 看全部")
        d = lv[key]
        label = f"{key}" + (f" · {d['variant']}" if d["variant"] else "")
        refs.append((label, d["model"]))
        if a.fallbacks:
            for i, f in enumerate(d["fallbacks"], 1):
                refs.append((f"{key} fallback#{i}", f))
    elif a.all:
        seen = set()
        for name in sorted(lv):
            m = lv[name]["model"]
            if m and m not in seen:
                seen.add(m)
                refs.append((name, m))
    elif a.model:
        refs.append(("--model", a.model))
    else:
        slot = a.slot or "model"
        ref = cfg.get(slot)
        if not ref:
            raise SystemExit(f"opencode.json 没有顶层 {slot!r}（试 --level 或 --list）")
        refs.append((slot, ref))

    print(f"配置目录 {a.config_dir}   要跑 {len(refs)} 个")
    ok = fail = 0
    for label, ref in refs:
        base, key, model, dialect, pid = resolve(cfg, ref)
        suffix, payload = build(dialect, model, a.stream, a.prompt)
        url = base + suffix
        show(label, ref, base, key, model, dialect, pid, payload, url)
        if a.dry:
            continue
        if call(url, key, dialect, payload, a.timeout):
            ok += 1
        else:
            fail += 1

    if not a.dry:
        print(f"\n结果: \033[32m{ok} 成功\033[0m, \033[31m{fail} 失败\033[0m")
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
