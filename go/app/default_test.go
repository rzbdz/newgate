package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// TestDefaultGraphStartsWithConfigGatewayAndConfigHook 锁住「地基先起」：
// config / config-hook / gateway 三个提供者必须排在**所有消费者**之前。
//
// 为什么不是「排在最前三个」：2026-09-17 加了 modules/breaker —— 它没有任何
// Requires（只认 binding 键、状态码和延迟），拓扑上是叶子，字母序又正好在
// `config` 前面，于是合法的排到了首位。地基的定义是「先于消费者」，不是
// 「绝对第一」，所以断言写成与消费者的相对位置。
//
// 只断言这三个的**集合**先于消费者，不锁它们之间的相对顺序：装配清单是构建期
// 扫描 modules/ 按字母序生成的（app/modules_gen.go），三者互相独立、拓扑排序
// 遇到并列时按声明位置决胜，相对顺序因此是生成顺序的副产品，不是设计意图。
func TestDefaultGraphStartsWithConfigGatewayAndConfigHook(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	names := app.ComponentNames()
	index := map[string]int{}
	for i, name := range names {
		index[name] = i
	}

	foundation := []string{"config", "config-hook", "gateway"}
	consumers := []string{
		"cli", "runtime", "deepseek", "glm", "opencode", "opencode-omo",
		"thinking", "claudecode", "claudecode-deepseek", "claudecode-glm", "wrapper",
	}
	last := -1
	for _, name := range foundation {
		at, ok := index[name]
		if !ok {
			t.Fatalf("component order = %v; 缺少地基 %s", names, name)
		}
		if at > last {
			last = at
		}
	}
	for _, name := range consumers {
		at, ok := index[name]
		if !ok {
			t.Fatalf("component order = %v; 消费者 %s 不在图里", names, name)
		}
		if at < last {
			t.Fatalf("component order = %v; 消费者 %s 排在地基之前（地基最晚在第 %d 位）",
				names, name, last)
		}
	}

	if got, want := app.Names(), []string{"claude", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
	opencode, ok := app.Get("opencode")
	if !ok || opencode.Config == nil {
		t.Fatal("opencode takeover was not injected by opencode-omo")
	}
}

func TestGatewayReceivesModuleHooks(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	var names []string
	for _, plugin := range special.Plugins() {
		names = append(names, plugin.Name())
	}
	for _, want := range []string{
		"claude-bg", "claudecode-deepseek", "deepseek", "glm", "always-thinks",
	} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("gateway hooks = %v, missing %s", names, want)
		}
	}
}
