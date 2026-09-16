// Package opencodeomo 把 Oh My OpenAgent 作为 OpenCode 的可选扩展接入。
//
// OMO 同时涉及三类边界：改写它自己的配置文件、向 config 贡献动态角色、
// 向 CLI 贡献命令和诊断。它们由一个组件共同持有 Release，确保接管和注册
// 可以完整撤销，但不会让 config、CLI 或基础 opencode 模块了解 OMO 格式。
package opencodeomo

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"

	modules "github.com/rzbdz/newgate/go/component"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/api"
	configapi "github.com/rzbdz/newgate/go/modules/config/api"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	opencodeapi "github.com/rzbdz/newgate/go/modules/opencode/api"
)

type configTakeover struct{}

var _ agentapi.ConfigTakeover = (*configTakeover)(nil)

// Takeover 创建 OMO 配置接管适配器；返回接口以隐藏无状态实现细节。
func Takeover() agentapi.ConfigTakeover { return configTakeover{} }

// New 声明 OMO 集成组件：绑定 OpenCode 配置接管、贡献额外档位，
// 并把自身诊断与命令作为 many capability 交给 CLI 聚合。
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "opencode-omo",
		Requires: []modules.Requirement{
			modules.Need(configapi.Capability),
			modules.Need(agentapi.ConfigHooksCapability),
			modules.Need(opencodeapi.Capability),
		},
		Provides: []modules.Provision{
			modules.Provide(cliapi.DiagnosticsCapability,
				cliapi.DiagnosticProvider(diagnostics{})),
			modules.Provide(cliapi.CommandsCapability,
				cliapi.Command(omoCommand{})),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			config := modules.MustGet(ctx, agentapi.ConfigHooksCapability)
			configStore := modules.MustGet(ctx, configapi.Capability)
			client := modules.MustGet(ctx, opencodeapi.Capability)
			release, err := config.BindTakeover(client.AgentID, Takeover())
			if err != nil {
				return err
			}
			releases = append(releases, release)
			release, err = configStore.RegisterRoleProvider(omoRolesProvider{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// Targets 把接口调用转给当前环境的目标发现逻辑。
func (configTakeover) Targets() []string { return TargetFiles() }

// IsTakenOver 检查单个目标是否已经指向 newgate。
func (configTakeover) IsTakenOver(target string) bool {
	return IsTakenOver(target)
}

// Apply 对所有目标执行可报告、可恢复的接管。
func (configTakeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	return ApplyAll(port)
}

// Restore 撤销该适配器拥有的配置改写。
func (configTakeover) Restore() ([]string, error) { return RestoreAll() }

// SlotsFile 返回 OMO 动态档位的 newgate 自有配置路径。
func SlotsFile() string { return filepath.Join(paths.Config(), "omo-slots.json") }

// TargetFiles 按 OpenCode/XDG 约定定位需要接管的配置，
// 并优先选择用户已经存在的 OMO 文件，避免无意创建第二份配置。
func TargetFiles() []string {
	if dir := os.Getenv("NEWGATE_TARGET_DIR"); dir != "" {
		return []string{
			filepath.Join(dir, "opencode.json"),
			filepath.Join(dir, "oh-my-openagent.json"),
		}
	}
	home := paths.Home()
	configHome := filepath.Join(home, ".config")
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		configHome = value
	}
	out := []string{filepath.Join(configHome, "opencode", "opencode.json")}
	for _, candidate := range []string{
		filepath.Join(configHome, "opencode", "oh-my-openagent.json"),
		filepath.Join(configHome, "oh-my-openagent", "oh-my-openagent.json"),
		filepath.Join(home, ".oh-my-openagent.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return append(out, candidate)
		}
	}
	return append(out, filepath.Join(configHome, "opencode", "oh-my-openagent.json"))
}

func writeAtomicMode(path string, content []byte, mode os.FileMode) error {
	tmp := path + ".newgate.tmp"
	if err := ioutil.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
