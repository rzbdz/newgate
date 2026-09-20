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
