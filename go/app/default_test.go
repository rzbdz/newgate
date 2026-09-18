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
// 2026-09-18：命令/诊断/状态行三本账从 cli 下沉到了 **surface**（叶子模块），
// 所以那一侧的所有者从 cli 换成了 surface。这不是改名，是解开一个环：账本长在
// cli 上时，「想贡献命令」就必须 Need(cli)，而 cli 自己又要 Need(runtime /
// config / gateway) 才能渲染与启动客户端——两条边首尾相接，于是 `newgate start`
// / `tier` / `probe` 这些命令永远搬不回自己的模块。详见 modules/surface 的包注释。
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
		// owner → 注册者：命令账本搬去 surface 之后，这几条都指向 surface。
		{"surface", "cli", "cli 从这里取命令来分派与排版"},
		{"surface", "gateway", "gateway 注册 st / schema-repair / debug"},
		{"surface", "claudecode", "claudecode 注册 naked"},
		{"surface", "plugin-manager", "plugin-manager 注册 plugin"},
		{"surface", "opencode-omo", "omo 注册 omo"},
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

// TestNothingDependsOnCLI 锁住「cli 是一个纯界面」。
//
// 这是 2026-09-18 那次拆分要守住的**结果**：拆分之前，想贡献命令的模块必须
// Need(cli)，于是 cli 成了所有模块的下游，而它自己又要 Need(runtime/config/
// gateway) —— 环。拆出 surface 之后 cli 只被组合根（main）用，**没有任何模块
// 依赖它**。
//
// 为什么值得一条测试：这个环不是靠一次决心解开的，是被一条一条具体的依赖重建
// 起来的。哪天有人为了图方便写下 `modules.Need(cliapi.Capability)`，环会静默
// 回来——症状是「又一个命令搬不出去了」，而那要等到有人真的想搬的时候才发现。
// 在这里红，比在那里红早得多。
func TestNothingDependsOnCLI(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	for _, c := range app.Components() {
		if c.Name == "cli" {
			continue
		}
		for _, req := range c.Requires {
			if req.Name() == "cli" {
				t.Fatalf("%s 依赖了 cli：cli 是界面，不该被任何模块依赖——"+
					"要贡献命令请依赖 surface（modules.Need(surface.Capability)）", c.Name)
			}
		}
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
