package opencodeomo

import (
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
type moduleProvider struct{}

var _ agentapi.ConfigTakeover = (*configTakeover)(nil)
var _ modules.Provider = (*moduleProvider)(nil)

func Takeover() agentapi.ConfigTakeover { return configTakeover{} }

func New() modules.Provider { return moduleProvider{} }

func (moduleProvider) Component() modules.Component {
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
		Start: func(ctx modules.Context) error {
			config := modules.MustGet(ctx, agentapi.ConfigHooksCapability)
			if err := config.BindTakeover("opencode", Takeover()); err != nil {
				return err
			}
			config.RegisterRoleProvider(omoRolesProvider{})
			return nil
		},
	}
}

func (configTakeover) Targets() []string { return TargetFiles() }
func (configTakeover) IsTakenOver(target string) bool {
	return IsTakenOver(target)
}
func (configTakeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	return ApplyAll(port)
}
func (configTakeover) Restore() ([]string, error) { return RestoreAll() }

func SlotsFile() string { return filepath.Join(paths.Config(), "omo-slots.json") }

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
