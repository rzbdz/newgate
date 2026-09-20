package app

import (
	"context"
	"testing"

	modules "github.com/rzbdz/newgate/go/component"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook"
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
	catalog := modules.MustGet(second.Context(), agentapi.AgentCatalogCapability)
	if got := catalog.Names(); len(got) != 2 {
		t.Fatalf("第二次装配的客户端目录 = %v，期望恰好 [claude opencode]", got)
	}
}

// TestGraphCoversEveryModule 把「生成器扫到的组件」和「图里真启动的组件」
// 对上账。
//
// 生成器只看静态文件（有 module.go + 导出 New()），图是运行期真装配的结果。
// 两者不一致意味着某个模块被扫进来了却在图上缺席——那要么是它没提供任何
// 东西（无害但可疑），要么是接口对不上（危险）。这里用名字集合对齐。
//
// **root 要对上的是另一本账**（2026-09-20）：它是唯一的 built-in，不参与 modules/
// 扫描（见 go/root 的包注释），所以「图 = 扫描清单」这条等式现在写成
// 「图 = 扫描清单 + built-in」。这条等式仍然是有意义的断言：它保证**没有第二个
// 模块**绕过扫描被偷偷装进来。
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
	want := len(generatedComponents()) + len(builtinComponents())
	if got := len(names); got != want {
		t.Fatalf("图里 %d 个组件，扫描清单 %d + built-in %d = %d",
			got, len(generatedComponents()), len(builtinComponents()), want)
	}
	if !names["root"] {
		t.Fatalf("唯一的 built-in 必须总在图里（实际 %v）", built.ComponentNames())
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
// 当时绕开那个环的办法另有其人：component.Inject（一条**不参与排序**的注入边）
// 加 Component.Attach（全图 Start 之后再跑一遍的注入阶段）。那套机制在
// 2026-09-18 晚些时候删掉了——环的真正解法是把 **ui 的出边砍干净**
// （cli.Requires 现在为空），出边没了以后 Optional(cli) 这条**入边**不可能成环，
// 于是注入可以回到 Start 里、回到一条正常的排序边上（见 component.Optional）。
//
// 所以这条不变量现在的写法是：**没有任何组件对界面声明一条非 Optional 的边**。
// 换句话说，对 ui 只允许弱依赖——装着就注册，不装就跳过；不允许 Need，那等于
// 宣称「没有界面我就活不了」，也就把界面重新拖回了依赖图里。
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
			if !requirement.Optional() {
				t.Fatalf("组件 %s 对 ui 声明了硬依赖（%s）——"+
					"界面依赖别人渲染、别人依赖界面注入，这就是那个环；"+
					"对 ui 请用 modules.Optional（弱依赖）", component.Name, requirement.Name())
			}
		}
	}
}

// TestInjectorsStartAfterTheUI 锁住「注入别人的人要等被注入的人」这条顺序。
//
// 这是用户 2026-09-18 的原话落到测试上：**注入别人的模块，一定要等那个被注入的
// 人加载完；被注入的人不该等注入他的人**。落到本仓库就是：往界面里注册命令与
// 状态行的模块，必须排在 `cli` 后面。
//
// # 为什么值得一条自己的测试
//
// 它以前**没有**保证。Inject（不参与排序）时代，9 个注入者的顺序纯粹是稳定拓扑
// 排序碰出来的——实测恰好都在 cli 之后，靠的是目录名字母序。任一次目录改名、或
// 加一个排在 cli 前面的注入者，顺序就反过来，而症状是「界面先起、命令后到」：
// 谁先谁后只在这一瞬间有差别，跑完就看不见了。
//
// 现在这条顺序由 Optional(cli) 这条**排序边**给出（见 component.Optional），
// 也就是由机制保证。这条测试守的是「机制真的还在」——有人把 Optional(cli) 换回
// Need(cli) 之外的写法、或者干脆删掉那条 Requires，这里就红。
//
// 「cli 先起」不等于「cli 早于一切」：cli 自己不依赖任何模块（TestCLIDependenciesOnlyShrink），
// 所以它就是拓扑序里最前面那几个之一，先于它意味着基本没什么要在它前面。
func TestInjectorsStartAfterTheUI(t *testing.T) {
	testkit.Sandbox(t)

	built, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	names := built.ComponentNames()
	at := map[string]int{}
	for i, name := range names {
		at[name] = i
	}
	ui, ok := at["cli"]
	if !ok {
		t.Fatalf("component order = %v; 图里没有 cli", names)
	}

	// 注入者名单从**声明**里读（谁 Optional(cli)），不是写死的清单：新模块加一条
	// 弱依赖就自动进这条断言，不需要回来改测试。
	injectors := 0
	for _, component := range built.Components() {
		for _, requirement := range component.Requires {
			if requirement.Name() != "cli" {
				continue
			}
			injectors++
			if at[component.Name] < ui {
				t.Fatalf("component order = %v; %s(%d) 排在 cli(%d) 之前 —— "+
					"它就是往界面里注册命令/状态行的那个模块，"+
					"排前面意味着它的 Start 跑的时候界面可能还没就绪",
					names, component.Name, at[component.Name], ui)
			}
		}
	}
	if injectors == 0 {
		t.Fatal("没有任何模块声明 Optional(cli) —— 界面的七本账全是空的，" +
			"要么是注入点被拆了，要么是这条依赖被误删了")
	}
}
