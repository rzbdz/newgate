package pluginmanager

import (
	"fmt"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/go/component"
)

// reported 是一个模块上报的自己。分类（Type）不在这里——它跟着组件定义走在
// component.Component 上，Modules() 把两者合起来（见那里的注释）。
type reported struct {
	name     string
	switches []Switch
}

// service 持有账本：模块自报的开关点（byName），加上组合根递进来的组件图（catalog）。
//
// 账本用 component.Registry[T] 而不是手写一张表：它零值可用、给单调 token、
// 能按 token 精确撤销、check 在写锁内跑（并发注册同一个名字只会有一个成功）。
// 这是 docs/09-extension-guide.md §3 要求的那条。
type service struct {
	mu      sync.Mutex
	byName  modules.Registry[reported]
	catalog []modules.Component
}

var _ Manager = (*service)(nil)

// RegisterSelf 见 Manager 的注释。这是模块**自愿**参与运行期开关的唯一入口。
func (s *service) RegisterSelf(name string, switches []Switch) (modules.Release, error) {
	if name == "" {
		return nil, fmt.Errorf("plugin-manager: 模块名不能为空")
	}
	// 先校验这条自己合不合法（不需要看别人的），再进查重。
	if err := validateSwitches(name, switches); err != nil {
		return nil, err
	}
	return s.byName.Register(reported{name: name, switches: switches}, func(existing []reported) error {
		for _, e := range existing {
			if e.name == name {
				return fmt.Errorf("plugin-manager: 模块 %q 已经上报过（每个模块只报一次）", name)
			}
		}
		for _, e := range existing {
			for _, have := range e.switches {
				for _, want := range switches {
					if have.Path == want.Path {
						return fmt.Errorf("plugin-manager: 开关点 %q 已被模块 %q 占用", want.Path, e.name)
					}
				}
			}
		}
		return nil
	})
}

// validateSwitches 校验单条开关点自身的合法性，只看这一条、不看别人的。
func validateSwitches(name string, switches []Switch) error {
	for _, sw := range switches {
		if sw.Path == "" {
			return fmt.Errorf("plugin-manager: 模块 %q 报了空 Path 的开关点", name)
		}
		// 前缀规则：路径必须长在模块名底下，这样「这个开关属于谁」不用查表就看得出来。
		if !strings.HasPrefix(sw.Path, name+".") {
			return fmt.Errorf("plugin-manager: 开关点 %q 必须以模块名 %q 加一个点开头（%s.<路径>）",
				sw.Path, name, name)
		}
		if sw.Title == "" || sw.Why == "" {
			return fmt.Errorf("plugin-manager: 开关点 %q 必须写清 Title（人话）与 Why（关掉会怎样）",
				sw.Path)
		}
		switch sw.Danger {
		case DangerSafe, DangerQuirk:
		case DangerFootgun:
			// 「不给无限期的 footgun」在这里做成结构性保证，而不是靠纪律：
			// 一个会破坏正确性的开关，注册不出来「永久开着」这种形态。
			if sw.TTL <= 0 {
				return fmt.Errorf("plugin-manager: 开关点 %q 是 footgun，必须带默认时限（TTL > 0）", sw.Path)
			}
		default:
			return fmt.Errorf("plugin-manager: 开关点 %q 的 Danger %q 不认识（safe/quirk/footgun）",
				sw.Path, sw.Danger)
		}
	}
	return nil
}

// Lookup 按 Path 找一条开关点。CLI 用它决定一次开关该写 Off 表还是 On 表。
func (s *service) Lookup(path string) (Switch, bool) {
	for _, r := range s.byName.All() {
		for _, sw := range r.switches {
			if sw.Path == path {
				return sw, true
			}
		}
	}
	return Switch{}, false
}

// SetCatalog 由组合根调用，见 Manager 的注释。
func (s *service) SetCatalog(components []modules.Component) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog = append([]modules.Component(nil), components...)
}

// Modules 合并两个来源，按组件启动顺序返回。
//
// **枚举源是组件图，不是注册表**——这一条是有意的：没参与开关体系的模块（不调
// RegisterSelf）必须照样列得出来，否则「newgate plugin 列出所有插件」就是假的，
// 而且「这个模块没有开关」和「这个模块忘了注册」会长得一模一样。
func (s *service) Modules() []Module {
	s.mu.Lock()
	catalog := append([]modules.Component(nil), s.catalog...)
	s.mu.Unlock()

	byName := map[string][]Switch{}
	for _, r := range s.byName.All() {
		byName[r.name] = r.switches
	}

	out := make([]Module, 0, len(catalog))
	seen := map[string]bool{}
	for _, c := range catalog {
		out = append(out, Module{Name: c.Name, Type: c.Type, Switches: byName[c.Name]})
		seen[c.Name] = true
	}
	// 上报过但不在图里的：组合根还没递图，或者模块名与组件名不一致。后者是配置
	// 错误——宁可显式列出来（归 others）也不要静默吞掉，静默正是这个功能要消灭的
	// 东西。app 层的测试会把它拦下来。
	var orphans []Module
	for _, r := range s.byName.All() {
		if seen[r.name] {
			continue
		}
		orphans = append(orphans, Module{Name: r.name, Type: TypeOthers, Switches: r.switches})
	}
	return append(out, orphans...)
}
