package component

import (
	"context"
	"strings"
	"testing"
)

// 图纸（Resolve）的全部价值是一句话：**不启动任何东西**。
//
// 为什么值得单独钉住：破了它不会红任何别的东西——图照样对、顺序照样对，症状
// 只是「求一张图，结果整个世界被初始化了一遍」（端口起来了、全局注册表被改了、
// 第二份规格书当场撞 already registered）。2026-09-20 架构图工具撞的就是这个。

type seed struct {
	capabilityName string
	provided       bool
	starts         *int
}

func (s seed) component(name string, requires ...Requirement) Component {
	c := Component{Name: name, Type: "test", Requires: requires}
	if s.provided {
		c.Provides = []Provision{Provide(NewCapability[struct{}](s.capabilityName), struct{}{})}
	}
	if s.starts != nil {
		n := s.starts
		c.Start = func(context.Context, Context) error { *n++; return nil }
	}
	return c
}

type fixedLoader []Component

func (f fixedLoader) Load() ([]Component, error) { return f, nil }

// TestResolveDoesNotStartAnything 是这一层存在的理由。
func TestResolveDoesNotStartAnything(t *testing.T) {
	starts := 0
	s := seed{capabilityName: "leaf", provided: true, starts: &starts}
	plan, err := Resolve(fixedLoader{
		s.component("leaf"),
		s.component("user", Need(NewCapability[struct{}]("leaf"))),
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Resolve 启动了 %d 个组件——图纸不该有生命周期", starts)
	}
	if got := plan.Names(); len(got) != 2 || got[0] != "leaf" || got[1] != "user" {
		t.Fatalf("顺序不对（被依赖的要在前）: %v", got)
	}
}

// TestPlanOrderMatchesStartOrder：图纸与真启动**必须**是同一个顺序，否则拿图纸
// 做的推论（谁在谁之后、谁会先拿到端口）全是假的。这条把两份实现钉在一起。
func TestPlanOrderMatchesStartOrder(t *testing.T) {
	// 声明顺序故意是反的（依赖者在前），这样「顺序对不对」才是个真问题。
	comps := []Component{
		{Name: "mid", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("leaf"))}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(NewCapability[struct{}]("leaf"), struct{}{})}},
	}
	plan, err := Resolve(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := New(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop(context.Background()) }()

	fromPlan, fromStart := plan.Names(), mgr.ComponentNames()
	if strings.Join(fromPlan, ",") != strings.Join(fromStart, ",") {
		t.Fatalf("图纸顺序 %v 与启动顺序 %v 不一致——两份实现开始漂了", fromPlan, fromStart)
	}
}

// TestResolveStillValidates：图纸不启动，但**校验一步不少**——缺 provider 的 Need
// 在这里就该报错（这正是「解析真的能过」这句话的含义）。
func TestResolveStillValidates(t *testing.T) {
	_, err := Resolve(fixedLoader{
		{Name: "user", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("nothing-provides-this"))}},
	})
	if err == nil {
		t.Fatal("缺 provider 的 Need 必须在解析期就报错")
	}
}

// TestDependenciesReportsBothKindsOfEdge：review 时最该看见的一条区别——Need 是
// 硬边（断了起不来），Optional 是弱边（断了只是没入口），而**没人提供的 Optional**
// 正是「这个发行版少装了什么」的答案。
func TestDependenciesReportsBothKindsOfEdge(t *testing.T) {
	plan, err := Resolve(fixedLoader{
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(NewCapability[struct{}]("leaf"), struct{}{})}},
		{Name: "user", Type: "test", Requires: []Requirement{
			Need(NewCapability[struct{}]("leaf")),
			Optional(NewCapability[struct{}]("ui")),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := plan.Dependencies()["user"]
	if len(deps) != 2 {
		t.Fatalf("user 该有两条依赖，实际 %+v", deps)
	}
	byCap := map[string]Dependency{}
	for _, d := range deps {
		byCap[d.Capability] = d
	}
	if d := byCap["leaf"]; d.Optional || d.ProvidedBy != "leaf" {
		t.Errorf("leaf 该是硬边且由 leaf 提供: %+v", d)
	}
	if d := byCap["ui"]; !d.Optional || d.ProvidedBy != "" {
		t.Errorf("ui 该是弱边且没人提供（没装界面）: %+v", d)
	}
}

// TestPlanComponentsIsACopy：拿到图纸的人不该能改到框架手里的那份。
func TestPlanComponentsIsACopy(t *testing.T) {
	plan, err := Resolve(fixedLoader{{Name: "leaf", Type: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Components()
	got[0].Name = "tampered"
	if plan.Names()[0] != "leaf" {
		t.Error("Components() 给出去的是内部切片——调用方能改坏图纸")
	}
}

// stalePlan 是「组件集合没变、边变了」的 Loader：它交出的组件与真装配一致，但
// 图纸是旧的。
type stalePlan struct {
	components []Component
	order      []string
}

func (s stalePlan) Load() ([]Component, error) { return s.components, nil }

func (s stalePlan) PrecomputedOrder() []string { return s.order }

// TestPrecomputedPlanRejectsAStaleOrder 锁住 2026-11-18 那条修补。
//
// 现场：一个已装配的依赖从 Optional 翻成 Need（或者新增一条 Need），而生成的
// 清单没重生成——组件**集合一个都没变**，旧校验只比集合，于是放行，按旧顺序
// 启动一个排在提供者前面的消费者。那条路是每一条 CLI 的默认路径，所以
// 「提供者先启动」这条唯一的硬保证当时没有任何东西在守。
//
// 一个只会静默给出错顺序的校验比没有校验更坏：图看起来是好的。
func TestPrecomputedPlanRejectsAStaleOrder(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	comps := []Component{
		{Name: "user", Type: "test", Requires: []Requirement{Need(leaf)}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
	}
	// 名字集合与组件表逐个对得上，只有顺序是反的——正是「清单过期」的形状。
	_, err := Resolve(stalePlan{components: comps, order: []string{"user", "leaf"}})
	if err == nil {
		t.Fatal("图纸里消费者排在提供者前面时必须报错（否则它会拿到零值或 panic）")
	}
	if !strings.Contains(err.Error(), "out of date") || !strings.Contains(err.Error(), "user") {
		t.Fatalf("报错要点名过期的那条边与消费者，实际: %v", err)
	}
}

// TestPrecomputedPlanAcceptsTheRightOrder：同一条图，顺序对了就必须照样走快路径
// ——修复不能把正常的那条路变成「每次都报错」。
func TestPrecomputedPlanAcceptsTheRightOrder(t *testing.T) {
	leaf := NewCapability[struct{}]("leaf")
	comps := []Component{
		{Name: "user", Type: "test", Requires: []Requirement{Need(leaf)}},
		{Name: "leaf", Type: "test", Provides: []Provision{Provide(leaf, struct{}{})}},
	}
	plan, err := Resolve(stalePlan{components: comps, order: []string{"leaf", "user"}})
	if err != nil {
		t.Fatalf("顺序正确时不该报错: %v", err)
	}
	if got := plan.Names(); len(got) != 2 || got[0] != "leaf" {
		t.Fatalf("顺序不对: %v", got)
	}
}

// TestPrecomputedPlanRejectsAMissingNeedProvider：集合对得上、但某个 Need 的端口
// 整张图里没人提供（另一个模块被摘掉却没重生成）——同样是过期，同样要说出来。
func TestPrecomputedPlanRejectsAMissingNeedProvider(t *testing.T) {
	_, err := Resolve(stalePlan{
		components: []Component{
			{Name: "user", Type: "test", Requires: []Requirement{Need(NewCapability[struct{}]("gone"))}},
		},
		order: []string{"user"},
	})
	if err == nil {
		t.Fatal("图纸说能起、图上却没人提供这个端口，必须报错")
	}
}

// TestPrecomputedPlanAllowsAnAbsentOptional：Optional 的提供者不在**不是**过期
// ——那正是「这个发行版没装它」。修复不能把它误判成故障。
func TestPrecomputedPlanAllowsAnAbsentOptional(t *testing.T) {
	if _, err := Resolve(stalePlan{
		components: []Component{
			{Name: "user", Type: "test", Requires: []Requirement{Optional(NewCapability[struct{}]("ui"))}},
		},
		order: []string{"user"},
	}); err != nil {
		t.Fatalf("没人提供的 Optional 是合法装配: %v", err)
	}
}

// TestProvidersAgreesWithGetOnMultipleProviders：两个提供者时，诊断（Providers）
// 与运行期（Get）必须指同一人。
//
// 以前 Providers 是 last-wins、Get 是 first-wins：多装一个同端口模块后，架构图
// 与 `newgate st` 会指着一个根本没在处理请求的模块——两个说法各说各的，谁也不会红。
func TestProvidersAgreesWithGetOnMultipleProviders(t *testing.T) {
	port := NewCapability[string]("port")
	comps := []Component{
		{Name: "first", Type: "test", Provides: []Provision{Provide(port, "from-first")}},
		{Name: "second", Type: "test", Provides: []Provision{Provide(port, "from-second")}},
	}
	plan, err := Resolve(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Providers()["port"]; got != "first" {
		t.Fatalf("Providers 说 %q 提供 port，但 Get 拿到的是 first", got)
	}
	if all := plan.AllProviders()["port"]; len(all) != 2 || all[0] != "first" || all[1] != "second" {
		t.Fatalf("AllProviders 要按启动顺序列出全部: %v", all)
	}
	got, ok := Get(Context{values: collectValues(plan.Components())}, port)
	if !ok || got != "from-first" {
		t.Fatalf("Get 应该是 first-wins，实际 %q/%v", got, ok)
	}
}

// TestPrecomputedAndResolvedAgreeOnValues：两条出图路径对「谁先」必须给同一个
// 答案——否则同一份二进制走不走快路径会改变行为。
func TestPrecomputedAndResolvedAgreeOnValues(t *testing.T) {
	port := NewCapability[string]("port")
	comps := []Component{
		{Name: "a", Type: "test", Provides: []Provision{Provide(port, "a")}},
		{Name: "b", Type: "test", Provides: []Provision{Provide(port, "b")}},
	}
	resolved, err := Resolve(fixedLoader(comps))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Resolve(stalePlan{components: comps, order: resolved.Names()})
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := Get(Context{values: resolved.values}, port)
	r2, _ := Get(Context{values: replayed.values}, port)
	if r1 != r2 {
		t.Fatalf("resolved 给 %q、precomputed 给 %q——快路径改变了行为", r1, r2)
	}
}
