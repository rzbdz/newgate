package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/check"
)

// The translation step **runs only on the developer's machine** (a rule the user set on
// 2026-09-20): it calls an LLM, and putting it in CI would mean every push might burn tokens
// and depends on a gateway that is running. CI only runs check (offline, free).
//
// It calls **the user's own newgate gateway** -- this tool is itself a newgate user: the tier
// chain, fallback and upstream quirk patches all apply as usual. By default it goes through
// `smt-deepseek/deepseek-flash` and **explicitly turns thinking off**: translation is a
// mechanical task, and making it think costs tokens and time, and the reasoning content would
// leak into the translation (the kernel modules/thinking behavior of "turn it down to low if it
// cannot be turned off" is the safety net here).

const (
	defaultEndpoint = "http://127.0.0.1:8899"
	defaultModel    = "smt-deepseek/deepseek-flash"
	defaultBatch    = 20
)

func cmdSync(args []string) error { return runTranslate(args, "sync") }
func cmdTranslate(args []string) error {
	return runTranslate(args, "translate")
}

func runTranslate(args []string, name string) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	c := commonFlags(fs)
	lang := fs.String("lang", "zh-Hans", "翻成哪门语言")
	endpoint := fs.String("url", envOr("NEWGATE_I18N_URL", defaultEndpoint), "网关地址（默认本机 8899）")
	model := fs.String("model", envOr("NEWGATE_I18N_MODEL", defaultModel), "用哪个模型")
	batch := fs.Int("batch", defaultBatch, "一次请求翻几条")
	dry := fs.Bool("dry-run", false, "只列出要翻什么，不调网关、不写文件")
	onlyKey := fs.String("key", "", "只翻这一条（源语言原文）")
	timeout := fs.Duration("timeout", 120*time.Second, "单次请求超时")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	if *lang == i18n.SourceLang {
		return fmt.Errorf("源语言（%s）不需要翻译：代码里写的就是它", i18n.SourceLang)
	}

	fresh, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	cat, err := loadOne(c.root, c.catalogDir, *lang)
	if err != nil {
		return err
	}

	// Pick what to translate: those with no translation; orphans (no longer in the source) are
	// reported separately, not translated.
	var todo []string
	var orphans []string
	for _, msg := range i18n.SortedKeys(fresh.Messages) {
		if e, ok := cat.Messages[msg]; ok && !e.Empty() {
			continue // 已有译文（人写的或机翻过的）一律不动
		}
		if *onlyKey != "" && msg != *onlyKey {
			continue
		}
		todo = append(todo, msg)
	}
	for _, msg := range i18n.SortedKeys(cat.Messages) {
		if _, ok := fresh.Messages[msg]; !ok {
			orphans = append(orphans, msg)
		}
	}
	if len(orphans) > 0 {
		fmt.Fprintf(os.Stderr, "i18n: 有 %d 条孤儿译文（源码里已经没有这条消息了，改了英文措辞？）——"+
			"它们不会被自动处理，请人工决定删掉还是改回去：\n", len(orphans))
		for _, msg := range orphans {
			fmt.Fprintf(os.Stderr, "  %s\n", msg)
		}
	}
	if len(todo) == 0 {
		fmt.Printf("i18n: %s 没有要翻的（共 %d 条）\n", *lang, len(cat.Messages))
		return nil
	}
	if *dry {
		fmt.Printf("i18n: 要翻 %d 条（dry-run）：\n", len(todo))
		for _, msg := range todo {
			fmt.Println("  " + msg)
		}
		return nil
	}

	client := &http.Client{Timeout: *timeout}
	done, failed := 0, 0
	for start := 0; start < len(todo); start += *batch {
		end := start + *batch
		if end > len(todo) {
			end = len(todo)
		}
		chunk := todo[start:end]
		got, err := translateChunk(client, *endpoint, *model, chunk, fresh)
		if err != nil {
			// One failed batch does not abandon the whole run: record this batch, continue to the next,
			// and report them all at the end. One network hiccup should not throw away dozens already
			// translated.
			fmt.Fprintf(os.Stderr, "i18n: 这一批失败（%d 条）：%v\n", len(chunk), err)
			failed += len(chunk)
			continue
		}
		for _, msg := range chunk {
			text, ok := got[msg]
			if !ok || strings.TrimSpace(text) == "" {
				fmt.Fprintf(os.Stderr, "i18n: 模型没给这一条的译文：%s\n", msg)
				failed++
				continue
			}
			if err := validate(msg, text, fresh); err != nil {
				fmt.Fprintf(os.Stderr, "i18n: 丢弃一条（%v）：%s\n", err, msg)
				failed++
				continue
			}
			e := cat.Messages[msg]
			if fresh.Messages[msg].Plural() {
				// Chinese does not distinguish singular and plural: writing only other is enough (pick in
				// lib/i18n will use it).
				e.Other = text
			} else {
				e.Text = text
			}
			e.Machine = true
			// reviewed stays false: machine translation is machine translation, and only review makes it
			// count. This is a fact for the next person, and it is the thing `check -strict` blocks
			// before a release.
			e.Reviewed = false
			cat.Messages[msg] = e
			done++
		}
		fmt.Fprintf(os.Stderr, "i18n: %d/%d\n", min(end, len(todo)), len(todo))
	}
	if err := writeCatalog(c.root, c.catalogDir, cat); err != nil {
		return err
	}
	fmt.Printf("i18n: 翻好 %d 条、失败 %d 条 → %s\n", done, failed,
		filepath.Join(c.catalogDir, *lang+".json"))
	if failed > 0 {
		return fmt.Errorf("%d 条没翻成（见上面每一行的原因）；重跑一次会只补这些", failed)
	}
	fmt.Println("i18n: 记得人工过一遍——机翻条目标着 machine，复核后改成 reviewed")
	return nil
}

// validate has two hard requirements for a machine translation: no placeholder may be missing,
// and do not hand the sentence back unchanged.
func validate(msg, text string, fresh i18n.Ledger) error {
	want := i18n.Placeholders(msg)
	if le := fresh.Messages[msg]; le.Plural() {
		want = i18n.Placeholders(le.Other)
	}
	got := i18n.Placeholders(text)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		return fmt.Errorf("占位符对不上（要 %v，给了 %v）", want, got)
	}
	for _, p := range want {
		if !strings.Contains(text, "{"+p+"}") {
			return fmt.Errorf("占位符 %s 丢了", p)
		}
	}
	return nil
}

// translateChunk asks the model to translate one batch.
func translateChunk(client *http.Client, endpoint, model string, chunk []string, fresh i18n.Ledger) (map[string]string, error) {
	type entry struct {
		Text   string `json:"text"`
		Plural string `json:"plural,omitempty"`
		Args   string `json:"args,omitempty"`
	}
	req := make(map[string]entry, len(chunk))
	for _, msg := range chunk {
		e := entry{Text: msg}
		if le := fresh.Messages[msg]; le.Plural() {
			e.Plural = le.Other
		}
		if args := fresh.Messages[msg].Args; len(args) > 0 {
			e.Args = strings.Join(args, ", ")
		}
		req[msg] = e
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(payload)},
		},
		// Explicitly turn thinking off: translation is a mechanical task. If the upstream cannot turn
		// it off, the kernel's modules/thinking turns it into "least thinking" form (see that
		// module's comments); no need to branch on it here.
		"thinking": map[string]string{"type": "disabled"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(endpoint, "/")+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("连不上网关（%s）——`newgate status` 看看它跑着没: %w", endpoint, err)
	}
	defer resp.Body.Close()
	payloadOut, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("网关回了 %d: %s", resp.StatusCode, truncate(string(payloadOut), 300))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payloadOut, &parsed); err != nil {
		return nil, fmt.Errorf("网关回的报文解不出来: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("网关没给候选")
	}
	return parseTranslation(parsed.Choices[0].Message.Content)
}

// parseTranslation digs the JSON out of the model output.
//
// Why dig: no matter how often you insist "output only JSON", the model occasionally wraps it
// in a JSON code fence or adds a sentence like "Sure, here are the translations". We do not
// argue with it here; just take from the first { to the last }.
func parseTranslation(content string) (map[string]string, error) {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("模型没按 JSON 回（%v）: %s", err, truncate(s, 200))
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var plain string
		if err := json.Unmarshal(v, &plain); err == nil {
			out[k] = plain
			continue
		}
		// It returned the value as an object (`{"text": …}`, or the plural `{"one": …, "other": …}`
		// ). Accept it: **the translation itself is right**, and a wrong shape is not worth
		// discarding a whole call -- besides, the field names in the prompt (args/plural) already
		// look like this shape, so the model following that pattern is not unreasonable.
		var forms struct {
			Text  string `json:"text"`
			One   string `json:"one"`
			Other string `json:"other"`
		}
		if err := json.Unmarshal(v, &forms); err != nil {
			return nil, fmt.Errorf("键 %q 的值既不是字符串、也不是译文对象: %s",
				k, truncate(string(v), 80))
		}
		// Chinese does not distinguish singular and plural: if there is other, use other -- that is
		// the sentence that holds for every n.
		switch {
		case forms.Text != "":
			out[k] = forms.Text
		case forms.Other != "":
			out[k] = forms.Other
		case forms.One != "":
			out[k] = forms.One
		}
	}
	return out, nil
}

const systemPrompt = `You translate the user-facing strings of "newgate", a local LLM gateway CLI for coding agents (Claude Code, OpenCode). Translate from English into Simplified Chinese.

Input: a JSON object mapping each English string to {"text": <the string>, "plural": <its plural form, if any>, "args": <placeholder names>}.
Output: a JSON object with exactly the same keys, each value being the translation. Nothing else — no prose, no markdown fences.

Rules:
- Keep every {placeholder} exactly as written: same name, same braces, same count.
- Keep these terms in English: profile, tier, agent, shim, PATH, provider, binding, chain, fallback, token, daemon, plugin, breaker, upstream, thinkcache, tool_use.
- The CLI's voice: terse declarative sentences, no subject pronouns, no politeness, no exclamation. Example: "Proxy" → "代理", "No such profile: {name}" → "没有叫 {name} 的档位".
- Use Chinese punctuation (：、（）), and keep the leading indentation if the English has it.
- Command names, flags, file names, metric keys and JSON field names stay in English.`

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
