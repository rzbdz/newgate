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

// TestCLIStartsBeforeItsExtensionModules 锁住注册型扩展的**依赖方向**。
//
// ComponentNames 是拓扑序（Manager 存的就是 resolve 排好的顺序），所以这条
// 断言测的是真实的启动顺序，不是声明顺序。
//
// 为什么值得单独立一条：2026-09-17 之前 opencodeomo 是往一个「命令端口」里
// Provide，那条边是 `opencode-omo → cli`（omo 先起，cli 后起并取一次快照）。
// 改成 Register 之后所有者必须是 cli，边反转成 `cli → opencode-omo`：cli 先
// 起，扩展模块在 Start 里拿到 cli 的 service 再注册。反转本身没问题，但**反转
// 错了会以一种很隐蔽的方式坏掉**——omo 在 cli 之前 Start，MustGet(cliapi.Capability)
// 当场 panic；而如果哪天有人改成 Optional 取，就变成静默少一条命令。
//
// 停止顺序随之反转成扩展先停、cli 后停，这正是撤销需要的方向（释放时目标
// 还活着），Manager 逆序收束天然保证。
func TestCLIStartsBeforeItsExtensionModules(t *testing.T) {
	testkit.Sandbox(t)

	built, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	index := map[string]int{}
	for i, name := range built.ComponentNames() {
		index[name] = i
	}
	for _, ext := range []string{"opencode-omo"} {
		at, ok := index[ext]
		if !ok {
			t.Fatalf("扩展模块 %s 不在图里（实际 %v）", ext, built.ComponentNames())
		}
		if index["cli"] >= at {
			t.Fatalf("cli 在第 %d 位、%s 在第 %d 位——扩展模块必须先于 cli 注册，反过来 cli 就还没起",
				index["cli"], ext, at)
		}
	}
}
