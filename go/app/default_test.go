package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// TestDefaultGraphLayering 锁住「owner 先于注册者」这条层级。
//
// 这是设计意图的声明，不是拓扑排序的复述：拓扑排序只会保证 Requires 成立，
// 而下面这张表说的是**我们认为谁该在谁前面**。加一条依赖把层级搞反（比如让
// config 去依赖某个客户端模块）时，这里会红。
//
// 2026-09-18：`cli` 从「消费者」一侧挪到了「owner」一侧。它之前被列在消费者里
// 是因为它 Require runtime/breaker；但自从各模块开始经 cli.RegisterCommand 贡献
// 自己的命令（gateway 的 `st`、claudecode 的 `naked`、plugin-manager 的 `plugin`、
// opencode-omo 的 `omo`），cli 就成了**命令 / 状态行 / 诊断三个扩展点的 owner**
// ——注册者必须排在 owner 之后，所以 cli 必须早于所有想露面的模块。它 Require
// runtime/breaker 只说明它在地基里排得靠后，不说明它是业务模块。
//
// 断言成「每一对 owner→注册者」的相对位置，而不是「前三个是谁」：装配清单是按
// 字母序扫描 modules/ 生成的，拓扑并列时按声明位置决胜，绝对顺序是生成顺序的
// 副产品，不是设计意图（2026-09-17 breaker 就因为字母序排到了首位）。
func TestDefaultGraphLayering(t *testing.T) {
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
	at := func(name string) int {
		i, ok := index[name]
		if !ok {
			t.Fatalf("component order = %v; %s 不在图里", names, name)
		}
		return i
	}

	pairs := []struct{ owner, registrant, why string }{
		{"config", "cli", "cli 读配置"},
		{"config", "runtime", "runtime 读配置"},
		{"config", "gateway", "gateway 读配置"},
		{"config", "opencode-omo", "omo 槽位读配置"},
		{"config-hook", "cli", "cli 用 AgentCatalog"},
		{"config-hook", "runtime", "runtime 用 AgentCatalog"},
		{"config-hook", "claudecode", "claudecode 注册 agent 与 state 字段"},
		{"config-hook", "plugin-manager", "plugin-manager 注册 state 字段"},
		{"breaker", "cli", "cli 展示与注入健康表"},
		{"breaker", "deepseek", "deepseek 记账"},
		{"runtime", "cli", "cli 调接管/注入"},
		{"runtime", "wrapper", "wrapper 懒启动代理"},
		// owner → 注册者：这几条是「命令住回各模块」之后的层级。
		{"cli", "gateway", "gateway 注册 st"},
		{"cli", "claudecode", "claudecode 注册 naked"},
		{"cli", "plugin-manager", "plugin-manager 注册 plugin"},
		{"cli", "opencode-omo", "omo 注册 omo"},
		{"gateway", "thinking", "thinking 注册请求插件"},
		{"gateway", "deepseek", "deepseek 注册请求插件"},
		{"gateway", "claudecode", "claudecode 注册请求插件"},
		{"gateway", "claudecode-deepseek", "交叉语义注册请求插件"},
		{"plugin-manager", "deepseek", "deepseek 上报开关点"},
		{"plugin-manager", "claudecode-deepseek", "交叉语义上报开关点"},
	}
	for _, p := range pairs {
		if at(p.owner) > at(p.registrant) {
			t.Fatalf("component order = %v; %s(%d) 排在 %s(%d) 之后，但它%s",
				names, p.owner, at(p.owner), p.registrant, at(p.registrant), p.why)
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
