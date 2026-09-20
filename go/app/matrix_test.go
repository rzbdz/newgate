package app

import (
	"context"
	"strings"
	"testing"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/component/entry"
	entrymod "github.com/rzbdz/newgate/go/modules/entry"
	"github.com/rzbdz/newgate/go/testing/testkit"
)

// 摘除矩阵：**只有声明自己是 built-in 的模块不可摘**，其余每一个都摘得掉。
//
// # 为什么这条不变式值得一套测试
//
// 「装一个模块 = 把目录复制进 modules/」还有另一半，而且那一半更容易坏：
// **不装一个模块 = 把它从清单里去掉**。坏起来的样子不是编译错误，是别的：
//
//   - 组合根 import 了某个具体模块 → 摘掉它，核心编译不过（这一半由
//     app/direction_test.go 的棘轮守着，症状在编译期，最幸运的一种）；
//   - 别的模块**在 Start 里伸手去拿**一个自己没声明的端口 → 摘掉提供者，
//     构图期不会失败（没人声明依赖），运行时才 panic。这是最坏的一种：
//     构建通过、测试通过、装出来的二进制在某个命令上炸。
//
// 第二条正是这套测试存在的理由。它逐个模块摘、逐个模块看**失败的方式对不对**：
// 摘一个模块要么装得起来，要么**因为别人硬依赖它**而当场失败并点名那个端口。
// 除此之外的失败（panic、含糊的错、组合根编译不过）都是设计错误。
//
// # 判据的边界
//
// 边界就是模块自己声明的 `Type`：只有 `entrymod.TypeBuiltin` 不可摘（它是内核
// 唯一认识的那个模块——提供入口账本，见 go/modules/entry）。这个边界不是这里
// 规定的——它是**被检查的**：如果哪天有人把某个业务模块也标成 built-in，下面的
// 矩阵会少测一个，而 TestTheOnlyBuiltinCannotBeRemoved 会发现「标着 built-in 的
// 那个模块并不是组合根依赖的那个」，两条一起红。
func TestRemovalMatrix(t *testing.T) {
	testkit.Sandbox(t)
	all := generatedComponents()

	victims := 0
	for _, victim := range all {
		if victim.Type == entrymod.TypeBuiltin {
			continue
		}
		victims++
		t.Run(victim.Name, func(t *testing.T) {
			dependents := hardDependents(all, victim)
			manager, err := modules.NewContext(context.Background(), staticLoader(without(all, victim.Name)))

			if err == nil {
				t.Cleanup(func() { _ = manager.Stop(context.Background()) })
				if len(dependents) > 0 {
					t.Fatalf("摘掉 %s 竟然装起来了，但 %v 声明了硬依赖它的端口——"+
						"说明那条依赖没进图（检查 Requires 里是不是写成了 Optional）",
						victim.Name, dependents)
				}
				return
			}

			// 装不起来是**预期**的一半，前提是有人硬依赖它。
			if len(dependents) == 0 {
				t.Fatalf("摘掉 %s 装不起来，可是没有任何模块声明硬依赖它——"+
					"有人在不声明的情况下伸手拿它提供的东西（就是本文档开头说的第二类坏法）：\n%v",
					victim.Name, err)
			}
			// 报错必须点名缺的那个端口：那是「因为依赖」的证据，而不是别的偶然原因。
			for _, provision := range victim.Provides {
				if strings.Contains(err.Error(), provision.Name()) {
					return
				}
			}
			t.Fatalf("摘掉 %s 的报错没有点名它提供的端口 %v，无法判断失败原因：\n%v",
				victim.Name, provisionNames(victim), err)
		})
	}

	if victims < 5 {
		t.Fatalf("只测了 %d 个模块——矩阵退化了（剩下的全是 built-in？）", victims)
	}
}

// TestTheOnlyBuiltinCannotBeRemoved 是上一条矩阵的补集：**唯一不可摘的那个**
// 摘掉之后必须当场失败，而且要说得出为什么。
//
// 为什么值得单独一条：矩阵跳过了 built-in，所以「built-in 真的不可摘吗」在那边
// 是**没有被检查的假设**。这里把它变成检查——摘掉 entry 之后，图必须因为缺
// `entry` 端口而拒绝启动。
//
// 它还负责把**两套说法对到一起**。今天「不可摘」有两个出处：
//
//	模块自己声明的分类  Type = entrymod.TypeBuiltin      （矩阵按它划边界）
//	组合根自己的依赖    提供 app.compositionRootPorts()  （Selection.Load 按它拒绝）
//
// 两者必须指向同一个组件。对不上时的症状很隐蔽：有人给一个业务模块标上 built-in，
// 矩阵于是**跳过**它（少测一个），而 Selection.Load 又允许关掉它（它不提供组合根
// 要的端口）——它成了唯一没人看着的模块，正好是这套测试要防的那类退化。
func TestTheOnlyBuiltinCannotBeRemoved(t *testing.T) {
	testkit.Sandbox(t)
	all := generatedComponents()

	var builtins []modules.Component
	for _, component := range all {
		if component.Type == entrymod.TypeBuiltin {
			builtins = append(builtins, component)
		}
	}
	if len(builtins) != 1 {
		var names []string
		for _, b := range builtins {
			names = append(names, b.Name)
		}
		t.Fatalf("built-in 应当有且只有一个（entry），实际 %v", names)
	}
	if name := builtins[0].Name; name != "entry" {
		t.Fatalf("built-in 是 %q，想要 entry（内核唯一认识的那个模块）", name)
	}
	if _, serves := servesCompositionRoot(builtins[0]); !serves {
		t.Fatalf("entry 标着 built-in，却没提供组合根自己要用的端口 %v——"+
			"那样 Selection.Load 会允许关掉它，矩阵又跳过它：它成了唯一没人看着的模块",
			compositionRootPorts())
	}
	// 反向也要成立：组合根要用的端口只能有一个来源。多一个提供者，构图期会以
	// 「重复提供」失败，而那时离「谁才是入口账本」这个问题已经很远了。
	for _, component := range all {
		if component.Name == builtins[0].Name {
			continue
		}
		if port, serves := servesCompositionRoot(component); serves {
			t.Fatalf("组件 %s 也提供了组合根要用的端口 %s——入口账本只能有一个来源",
				component.Name, port)
		}
	}

	_, err := modules.NewContext(context.Background(), staticLoader(without(all, "entry")))
	if err == nil {
		t.Fatal("摘掉 entry 竟然装起来了：入口账本没了，进程起得来但谁也不是入口")
	}
	if !strings.Contains(err.Error(), modules.Name(entry.Capability)) {
		t.Fatalf("摘掉 entry 的报错该点名 %q 端口（那是它提供的东西），实际：\n%v",
			modules.Name(entry.Capability), err)
	}
}

// TestDependencyFreeModulesLoadStandalone 是矩阵的另一半：**没有任何依赖声明的
// 模块，装进一张只有它自己的图也必须装配成功**。
//
// 判据是「声明里一条 Requires 都没有」，不是「我认为它不依赖别人」——名字从声明
// 里读，新模块加一条依赖就自动退出这条断言，不需要回来改测试。
//
// 它挡的是一种具体的错：模块在 Start 里伸手拿一个自己没声明的端口。那样的模块在
// 整图上活得很好（别人恰好提供了），一被单独装起来就 panic——而那时人往往已经
// 在排查别的问题了。
func TestDependencyFreeModulesLoadStandalone(t *testing.T) {
	all := generatedComponents()

	checked := 0
	for _, component := range all {
		if len(component.Requires) > 0 {
			continue
		}
		checked++
		t.Run(component.Name, func(t *testing.T) {
			testkit.Sandbox(t)
			graph := testkit.Start(t, component)
			if names := graph.Names(); len(names) != 1 || names[0] != component.Name {
				t.Fatalf("只有 %s 的图，实际装出 %v", component.Name, names)
			}
		})
	}
	if checked == 0 {
		t.Fatal("一个零依赖模块都没有——要么依赖声明被加满了，要么这条判据写错了")
	}
}

// staticLoader 把一张写死的组件表交给内核（矩阵的每一行都要一张少一个人的图）。
type staticLoader []modules.Component

func (l staticLoader) Load() ([]modules.Component, error) { return l, nil }

// hardDependents 返回声明了**硬依赖**（Need）victim 所提供端口的那些组件。
//
// 只看 Need：可选依赖在提供者缺席时本来就不建边，摘掉提供者不该让图装不起来。
func hardDependents(all []modules.Component, victim modules.Component) []string {
	provided := map[string]bool{}
	for _, provision := range victim.Provides {
		provided[provision.Name()] = true
	}
	var out []string
	for _, component := range all {
		if component.Name == victim.Name {
			continue
		}
		for _, requirement := range component.Requires {
			if !requirement.Optional() && provided[requirement.Name()] {
				out = append(out, component.Name)
				break
			}
		}
	}
	return out
}

func without(all []modules.Component, name string) []modules.Component {
	out := make([]modules.Component, 0, len(all))
	for _, component := range all {
		if component.Name != name {
			out = append(out, component)
		}
	}
	return out
}

func provisionNames(component modules.Component) []string {
	out := make([]string, 0, len(component.Provides))
	for _, provision := range component.Provides {
		out = append(out, provision.Name())
	}
	return out
}
