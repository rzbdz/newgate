package app

import (
	"context"
	"testing"

	"github.com/rzbdz/newgate/go/testing/testkit"
)

// TestGraphCanBeRebuiltAfterStop 是整套重构里最便宜、覆盖面最广的一条不变量：
// **Stop 必须完整撤销 Start 做过的注册**。
//
// 为什么值得单独立一条
//
// 每个模块的 Start 都往别处注册东西（gateway 挂插件、confighook 注册 Agent、
// config 注册角色提供者、cli 收命令）。这些注册是**进程级全局状态**，而 Stop
// 里漏掉一个 Release 的症状极其隐蔽：
//
//   - 单独跑任何一条测试都是绿的（一个进程里只装一次图）；
//   - 只有在同一个进程里装第二次时才炸，或者更糟——**不炸**，而是第二张图
//     拿到第一张图残留的注册，行为取决于上一张图装了什么。
//
// 所以这里装两次：第一次的残留会让第二次的 Start 撞上重复注册或拿到脏数据。
// 这条测试在 2026-09-17 的重构里当场抓出过角色提供者的残留（opencodeomo
// 的 omo 槽位在第二次装配时被判定为「键被 omo 和 omo 同时注册」）。
func TestGraphCanBeRebuiltAfterStop(t *testing.T) {
	testkit.Sandbox(t)

	first, err := New(context.Background())
	if err != nil {
		t.Fatalf("第一次装配失败: %v", err)
	}
	firstNames := first.ComponentNames()
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("第一次停止失败: %v", err)
	}

	second, err := New(context.Background())
	if err != nil {
		t.Fatalf("第二次装配失败（说明第一次的 Stop 没撤干净）: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop(context.Background()) })

	if got := second.ComponentNames(); len(got) != len(firstNames) {
		t.Fatalf("两次装配的组件数不同：%v vs %v", firstNames, got)
	}
	// 客户端目录也必须干净：第二次仍应只有 claude 和 opencode，不该翻倍。
	if got := second.Names(); len(got) != 2 {
		t.Fatalf("第二次装配的客户端目录 = %v，期望恰好 [claude opencode]", got)
	}
}

// TestGraphCoversEveryModule 把「生成器扫到的组件」和「图里真启动的组件」
// 对上账。
//
// 生成器只看静态文件（有 module.go + 导出 New()），图是运行期真装配的结果。
// 两者不一致意味着某个模块被扫进来了却在图上缺席——那要么是它没提供任何
// 东西（无害但可疑），要么是接口对不上（危险）。这里用名字集合对齐。
func TestGraphCoversEveryModule(t *testing.T) {
	testkit.Sandbox(t)

	built, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	names := map[string]bool{}
	for _, name := range built.ComponentNames() {
		if names[name] {
			t.Fatalf("组件名重复：%s（实际 %v）", name, built.ComponentNames())
		}
		names[name] = true
	}
	if got := len(names); got != len(generatedComponents()) {
		t.Fatalf("图里 %d 个组件，生成清单 %d 个", got, len(generatedComponents()))
	}

	// 地基与两条接管通路是产品定义的一部分，缺一个都跑不起来。
	for _, must := range []string{"config", "gateway", "config-hook", "runtime", "cli", "wrapper"} {
		if !names[must] {
			t.Fatalf("缺少必需组件 %s（实际 %v）", must, built.ComponentNames())
		}
	}
}

// TestUIStaysOutOfTheDependencyGraph 锁住「ui 不是依赖」这条不变量。
//
// 2026-09-18 之前这里断言的是「cli 排在扩展模块前面」——那时候注入是一条排序边
// （模块 Optional(cli)，要在自己的 Start 里拿到界面再注册）。那条边恰恰是死结的
// 来源：界面自己也要依赖那些模块（渲染要靠它们报数据），两边互指就成环，于是
// config / runtime / config-hook 三个模块永远注入不进来，命令只能被迫留在界面里。
//
// 现在注入走 component.Inject：**不排序**，全图 Start 完之后在 Attach 阶段交付。
// 所以这条不变量变成了「没有任何组件声明一条指向界面的非 late 边」——ui 装不装
// 只影响那些贡献有没有地方去，不影响任何一个模块的功能。
//
// 它比原来那条更强：原来只盯 opencode-omo 一个模块，现在任何模块将来对 ui 写错
// 依赖方向都会在这里红。
func TestUIStaysOutOfTheDependencyGraph(t *testing.T) {
	testkit.Sandbox(t)

	built, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	for _, component := range built.Components() {
		for _, requirement := range component.Requires {
			if requirement.Name() != "cli" {
				continue
			}
			if !requirement.Late() {
				t.Fatalf("组件 %s 对 ui 声明了一条排序边（%s）——"+
					"界面依赖别人渲染、别人依赖界面注入，这就是那个环；"+
					"注入请用 modules.Inject", component.Name, requirement.Name())
			}
		}
	}
}
