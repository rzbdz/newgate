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
// 命令/诊断/状态行三本账长在 **cli** 的 service 上，但注入**不是**一条排序边：
// 模块声明 modules.Inject(cli)，框架在全图 Start 完之后（Attach 阶段）把界面交给
// 它们（见 component.Inject）。所以下面这张表里没有「cli → 注入者」那一批——ui 从
// 依赖图里退出去了，装不装 ui 只影响「这些贡献有没有地方去」。
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
		{"config", "runtime", "runtime 读配置"},
		{"config", "gateway", "gateway 读配置"},
		{"config", "opencode-omo", "omo 槽位读配置"},
		{"config-hook", "runtime", "runtime 用 AgentCatalog"},
		{"config-hook", "claudecode", "claudecode 注册 agent 与 state 字段"},
		{"config-hook", "plugin-manager", "plugin-manager 注册 state 字段"},
		{"breaker", "deepseek", "deepseek 记账"},
		{"runtime", "wrapper", "wrapper 懒启动代理"},
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

// TestCLIDependenciesOnlyShrink 锁住「界面不依赖任何模块」这个方向。
//
// 目标**不是**「没人依赖界面」——那既不可能也不必要：模块要把命令注入界面，
// 本来就得先 Need 它。目标是反过来的那一侧：**界面自己不依赖任何人**，它只是
// 一个 ui，别人的东西是别人注入进来的回调，界面循环调用它们拿数据。
//
// 现在还没到那一步：下面允许名单里剩下的几个，是仍在界面手里的那批命令用的，
// 它们要一个个搬回各自的模块（start/stop 归 runtime、tier/profiles 归 config、
// probe/metrics 归 gateway…）。
//
// 这张表**只能变短**。往里加一项 = 把某条命令永远钉在界面里，而那正是这次重构
// 要拆掉的东西；症状要等到有人真想搬那条命令时才会显形（「搬不动，成环了」），
// 在这里红比在那里红早得多。
func TestCLIDependenciesOnlyShrink(t *testing.T) {
	// 名单现在是**空的**，而且它只该变小（2026-09-18 清到底）：
	//
	// 界面曾经依赖 config（读配置）、runtime（拉客户端）、agent-catalog（按 agent
	// 分派）、breaker（构造数据面）。四条边各自对应一批「界面知道某个模块的内部」：
	// tier/profile 的渲染、包装启动、agent 名单、健康表。它们全部搬回了拥有那些知识
	// 的模块，界面改成从注入点拿别人报上来的东西（命令、status 行、体检项、术语、
	// 诊断素材）。
	//
	// 所以这条断言现在的意思是：**界面一条出边都不许有**。任何新增的边都意味着又有
	// 一坨别人的知识回到了界面里。
	allowed := map[string]bool{}

	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	for _, c := range app.Components() {
		if c.Name != "cli" {
			continue
		}
		for _, req := range c.Requires {
			if !allowed[req.Name()] {
				t.Fatalf("界面依赖了 %q，而它不在允许名单里。界面应当**不依赖任何模块**："+
					"命令由拥有那项能力的模块注入（见 modules/cli/extension）。"+
					"要么把那条命令搬回它的模块，要么先想清楚为什么界面必须认识它。", req.Name())
			}
		}
		return
	}
	t.Fatal("图里没有 cli 组件")
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
