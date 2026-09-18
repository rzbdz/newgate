package surface

import (
	"fmt"

	modules "github.com/rzbdz/newgate/go/component"
	"github.com/rzbdz/newgate/go/modules/config/domain"
)

// service 是这一层的实现：三本账，各自一把锁（modules.Registry）。
//
// 零值可用（Registry 的零值就是一个空账本），所以模块的 Start 不需要做任何
// 初始化——账本本身没有生命周期，生命周期在**贡献者**那边：每次 Register 返回
// 一个 Release，由贡献者自己在 Stop 里逆序释放。
type service struct {
	commands    modules.Registry[Command]
	diagnostics modules.Registry[DiagnosticProvider]
	statuses    modules.Registry[StatusProvider]
}

var _ Surface = (*service)(nil)

// RegisterCommand 贡献一条命令；命令名撞车当场报错。
//
// 报错文案是中文：这条是**面向插件作者**的业务冲突（「我的命令名被谁占了」），
// 不是框架级装配错误，跟用户可见的文案保持一致。查重在 Registry.Register 的写锁
// 内跑，所以并发注册同一个名字也只会有一个成功——先到先得是这个功能最不该有的
// 行为（那样「谁占了这个名字」在清单里看不出来）。
func (s *service) RegisterCommand(command Command) (modules.Release, error) {
	if command == nil {
		return nil, fmt.Errorf("surface: 命令不能为 nil")
	}
	names := command.Names()
	if len(names) == 0 {
		return nil, fmt.Errorf("surface: 命令必须至少声明一个名字（Names）")
	}
	return s.commands.Register(command, func(existing []Command) error {
		for _, other := range existing {
			for _, have := range other.Names() {
				for _, want := range names {
					if have == want {
						return fmt.Errorf("surface: 命令名 %q 已被占用", want)
					}
				}
			}
		}
		return nil
	})
}

// RegisterDiagnostics 贡献一组 doctor 输出。诊断可叠加，不查重。
func (s *service) RegisterDiagnostics(provider DiagnosticProvider) (modules.Release, error) {
	if provider == nil {
		return nil, fmt.Errorf("surface: 诊断提供者不能为 nil")
	}
	return s.diagnostics.Register(provider, nil)
}

// RegisterStatus 贡献 `newgate status` 里的若干行。与诊断同理：可叠加、不查重。
func (s *service) RegisterStatus(provider StatusProvider) (modules.Release, error) {
	if provider == nil {
		return nil, fmt.Errorf("surface: 状态提供者不能为 nil")
	}
	return s.statuses.Register(provider, nil)
}

// Commands 当前全部命令，按注册顺序。
//
// **现取，不是启动时拍快照**：扩展模块可能比 CLI 晚一步才注册（启动顺序由
// 依赖图决定），拍快照会漏掉它们。
func (s *service) Commands() []Command { return s.commands.All() }

// Lookup 按名字找一条命令。Names 里的每个别名都能命中。
func (s *service) Lookup(name string) (Command, bool) {
	for _, command := range s.commands.All() {
		for _, candidate := range command.Names() {
			if candidate == name {
				return command, true
			}
		}
	}
	return nil, false
}

// Diagnostics 收集全部模块贡献的 doctor 输出。
func (s *service) Diagnostics() []Diagnostic {
	var out []Diagnostic
	for _, provider := range s.diagnostics.All() {
		out = append(out, provider.Diagnostics()...)
	}
	return out
}

// Statuses 收集全部模块贡献的 status 行。
func (s *service) Statuses(st *domain.State) []StatusLine {
	var out []StatusLine
	for _, provider := range s.statuses.All() {
		out = append(out, provider.Status(st)...)
	}
	return out
}
