package app

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/component/entry"
	modscan "github.com/rzbdz/newgate/go/tools/genmodules/scan"
)

// CoreModules 报的是「内核有哪些模块」，磁盘上 modules/ 是同一件事的另一份说法。
// 两者必须逐字一致——不一致的症状是消费者按目录名关模块时关了个不存在的东西。
func TestCoreModulesMatchesScannedDirs(t *testing.T) {
	want, err := modscan.Dirs(filepath.Join(repoDir(t), "..", "modules"))
	if err != nil {
		t.Fatalf("扫描 modules/：%v", err)
	}
	entries := CoreModules()
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Dir == "" {
			t.Fatalf("装配表里有个没写目录名的组件（组件名 %q）——目录名是关掉它的唯一凭据", e.Component.Name)
		}
		if e.Component.Name == "" {
			t.Fatalf("装配表里 %q 这个目录交出了一个没名字的组件", e.Dir)
		}
		got = append(got, e.Dir)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CoreModules 的目录集合与扫描结果不一致\n表里:  %v\n磁盘上: %v", got, want)
	}

	// 组件名不能重复：重复的话构图期才会以一句 `duplicate component` 失败，
	// 而那时离「表里多写了一行」已经很远了。
	seen := map[string]string{}
	for _, e := range entries {
		if prev, dup := seen[e.Component.Name]; dup {
			t.Errorf("组件 %q 出现了两次（目录 %s 与 %s）", e.Component.Name, prev, e.Dir)
		}
		seen[e.Component.Name] = e.Dir
	}
}

// Selection{} 是「内核全都要」，它必须与内核自己那张图逐组件相同。
func TestEmptySelectionEqualsKernelGraph(t *testing.T) {
	sel, err := Selection{}.Load()
	if err != nil {
		t.Fatalf("Selection{}.Load(): %v", err)
	}
	kernel, err := Loader{}.Load()
	if err != nil {
		t.Fatalf("Loader{}.Load(): %v", err)
	}
	if !reflect.DeepEqual(names(sel), names(kernel)) {
		t.Errorf("空选择装出来的图与内核自带图不同\n空选择: %v\n内核:   %v", names(sel), names(kernel))
	}
}

// 关掉一个内核模块：按**目录名**关得掉，而且只关掉那一个。
func TestSelectionDisablesByDirName(t *testing.T) {
	sel, err := Selection{Disable: []string{"cli"}}.Load()
	if err != nil {
		t.Fatalf("关掉 cli：%v", err)
	}
	if contains(names(sel), "cli") {
		t.Error("disable 点了 cli，图里还有 cli")
	}
	// 别的模块一个都不该少：关掉界面之后内核剩下的一切照常（docs/03 的
	// 「没装任何 ui 时一切功能照常」）。
	kernel, err := Loader{}.Load()
	if err != nil {
		t.Fatalf("Loader{}.Load(): %v", err)
	}
	if len(sel) != len(kernel)-1 {
		t.Errorf("关掉一个模块后组件数 = %d，想要 %d", len(sel), len(kernel)-1)
	}
}

// 名字写错的三种写法都必须报错——它们最坏的症状是「以为关了，其实在跑」。
func TestSelectionRejectsUnknownNames(t *testing.T) {
	cases := []struct {
		name string
		sel  Selection
		want string // 错误里必须出现的话
	}{
		{"点了不存在的目录", Selection{Disable: []string{"nope"}}, "nope"},
		{"写了组件名而不是目录名", Selection{Disable: []string{"config-hook"}}, "config-hook"},
		{"点了组合根自己要用的那个模块", Selection{Disable: []string{"entry"}}, "entry"},
		{
			"点了 Extra 里的模块（不要它就别列进去）",
			Selection{Extra: []Entry{{Dir: "hello", Component: helloProbe()}}, Disable: []string{"hello"}},
			"hello",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.sel.Load()
			if err == nil {
				t.Fatalf("没报错——这正是最坏的那种：用户以为关掉了，它其实还在跑")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误里没点名 %q，实际是：%v", c.want, err)
			}
		})
	}
}

// 重复装配、没写目录名、以及「把内核模块全关掉」也必须在装配前就报错。
func TestSelectionRejectsDuplicatesAndEmptyGraph(t *testing.T) {
	core := CoreModules()
	if len(core) == 0 {
		t.Fatal("内核一个模块都没有？")
	}
	dup := Selection{Extra: []Entry{{Dir: "shadow", Component: core[0].Component}}}
	if _, err := dup.Load(); err == nil {
		t.Error("同一个组件装了两次，没报错")
	}
	anonymous := Selection{Extra: []Entry{{Dir: "", Component: core[0].Component}}}
	if _, err := anonymous.Load(); err == nil {
		t.Error("Extra 里没写目录名，没报错")
	}

	// 全关掉：**必须**报错，而且得是因为组合根自己要用的那个关不掉——
	// 不是"关成功了然后装出一张空图"（空图跑起来只会让人以为命令坏了）。
	all := make([]string, 0, len(core))
	for _, e := range core {
		all = append(all, e.Dir)
	}
	_, err := Selection{Disable: all}.Load()
	if err == nil {
		t.Fatal("把所有内核模块都关掉，没报错")
	}
	if !strings.Contains(err.Error(), modules.Name(entry.Capability)) {
		t.Errorf("全关掉的报错该点名组合根要的那个端口 %q，实际：%v", modules.Name(entry.Capability), err)
	}
}

// helloProbe 冒充一个"消费者自己的模块"（Extra 那条路上来的），用来验证
// 「disable 管不着 Extra」那条拒绝。
func helloProbe() modules.Component {
	return modules.Component{Name: "hello-probe", Type: "example"}
}

// Extra 装在自带的后面——顺序不是依赖声明，但「消费者自己的模块排在后面」让
// 内核的错先于发行版的错暴露。
func TestSelectionAppendsExtraLast(t *testing.T) {
	probe := CoreModules()[0].Component
	probe.Name = "extra-probe" // 换个名字，免得撞上「重复组件」那条
	sel, err := Selection{Extra: []Entry{{Dir: "extra-probe", Component: probe}}}.Load()
	if err != nil {
		t.Fatalf("装一个 extra：%v", err)
	}
	if got := sel[len(sel)-1].Name; got != "extra-probe" {
		t.Errorf("最后一个组件是 %q，想要 extra-probe", got)
	}
}

func names(cs []modules.Component) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
