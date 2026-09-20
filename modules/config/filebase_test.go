package config

import (
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 原文那一栏（`kind: code`）是**看**那份文件的地方：报出盘上的字节，把凭据脱敏，
// 并且不给写。
//
// 2026-09-20 这里曾经有一条「拿卡片自己报的基线去写必须成功」的回归测试——那时原文
// 是可写的，而它丢过一次 `base` 字段，症状是「改原文点保存永远失败、改控件却好好
// 的」。2026-09-21 把那一半关掉之后（一份文件只有一个可写的面），那条断言连同
// `fileData.Base` 一起没有了对象：一个只看不写的面板不需要基线。

func TestTheRawHalfShowsTheFile(t *testing.T) {
	testkit.Sandbox(t)
	seedFile(t, "demo.kv", "desc=demo\nnormal=p/m\n")

	var code view.Concept
	for _, c := range fileConcepts() {
		if c.ID == "config.file.mappings/demo.kv" {
			code = c
		}
	}
	if code.ID == "" {
		t.Fatal("没报出 mappings/demo.kv 那张原文卡")
	}
	data, ok := code.Data.(fileData)
	if !ok {
		t.Fatalf("Data 不是 fileData，是 %T", code.Data)
	}
	if data.Path != "mappings/demo.kv" {
		t.Errorf("路径该是配置根下的相对路径，实际 %q", data.Path)
	}
	if data.Text != "desc=demo\nnormal=p/m\n" {
		t.Errorf("该原样报出盘上的字节，实际 %q", data.Text)
	}
	if data.Redacted {
		t.Error("这份文件里没有凭据，不该标成脱敏")
	}
}

// TestTheRawHalfRedactsCredentials：带凭据的文件，原文那一栏里的 key 必须是 ***。
//
// 这一条与「只读」是两件事，所以分开验：只读是**一份文件只有一个可写的面**，
// 而脱敏是**值根本不出去**——就算哪天原文那一半又能写了，凭据也不能进快照
// （浏览器没有的东西，就不可能被原样写回来）。
func TestTheRawHalfRedactsCredentials(t *testing.T) {
	testkit.Sandbox(t)
	if err := os.WriteFile(paths.ProvidersFile(),
		[]byte(`{"providers":{"p":{"api_key":"sk-secret"}}}`), 0o660); err != nil {
		t.Fatal(err)
	}

	c := findConcept(t, "config.file.providers.json")
	data, ok := c.Data.(fileData)
	if !ok {
		t.Fatalf("Data 不是 fileData，是 %T", c.Data)
	}
	if !data.Redacted {
		t.Fatal("带凭据的文件该标成脱敏")
	}
	if strings.Contains(data.Text, "sk-secret") {
		t.Errorf("凭据漏进快照了: %q", data.Text)
	}
}

// TestTheRawHalfIsAViewer：**一份文件只有一个可写的面**——原文那一半是只读的。
//
// 这里列的每一份文件都已经有结构化的编辑面（state.json 有开关卡、providers.json 有
// provider 表、mappings/* 各有一张档位卡），所以原文那一半的用处是**看**：对齐字段
// 名、抄一行出去、确认刚才那一下到底写成了什么。
//
// 为什么把那一半关掉（2026-09-21 拆掉的那一整套）：两半都可写时，同一份文件有两个
// 草稿、两个基线，于是要有「谁后改的说了算」、要挤出输的那一半的草稿、还要让输的
// 那一半跟着显示赢的那一半的内容——为此长出了内核的 Preview 契约、BFF 的
// /api/preview、前端的防抖与预览表。它们没有一个与「配置」有关，而每一个都可能
// 悄悄吃掉用户的改动（实测到两次）。
//
// 这条同时是**那套东西没有再长回来**的看门人：哪天有人给 fileConcepts 加回 Apply，
// 这里立刻红。
func TestTheRawHalfIsAViewer(t *testing.T) {
	testkit.Sandbox(t)
	seedFile(t, "demo.kv", "desc=demo\nnormal=p/m\n")
	// 另外两份也要种出来：读不出来的文件按设计报**坏卡**（那是另一条规矩），
	// 而这条要验的是「有结构化卡的那些文件，原文那一半只读」。注意这两份住在
	// **配置根**，不是 mappings/。
	for _, f := range []string{paths.ProvidersFile(), paths.StateFile()} {
		if err := os.WriteFile(f, []byte(`{"port":8899}`), 0o660); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range []string{
		"config.file.mappings/demo.kv",
		"config.file.state.json",
		"config.file.providers.json",
	} {
		c := findConcept(t, id)
		if c.Broken != "" {
			t.Errorf("%s 不该是坏的: %s", id, c.Broken)
			continue
		}
		if c.Apply != nil {
			t.Errorf("%s 是可写的——原文那一半只该是**看**那份文件的地方（一份文件只有一个可写的面）", id)
		}
	}

	// 反过来的那一半也得在：结构化那张卡仍然可写，否则这份文件就没人能改了。
	if c := findConcept(t, "config.profile.demo"); c.Apply == nil {
		t.Error("档位卡该是可写的——写入口关了原文那一半，就得由它一个顶上")
	}
}
