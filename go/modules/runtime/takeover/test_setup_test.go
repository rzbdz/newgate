package takeover

import (
	"os"
	"sort"
	"testing"

	agentapi "github.com/rzbdz/newgate/go/modules/confighook"

	"github.com/rzbdz/newgate/go/modules/claudecode"
	"github.com/rzbdz/newgate/go/modules/opencode"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
)

type testCatalog map[string]*agentapi.Agent

func (catalog testCatalog) Get(id string) (*agentapi.Agent, bool) {
	agent, ok := catalog[id]
	return agent, ok
}

func (catalog testCatalog) Names() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// fakeConfigTakeover 是测试专用的最小 ConfigTakeover：这个包测的是
// shim/config 两种接管机制怎么选、怎么切换，不测某个具体客户端插件的
// 改写逻辑，所以不该依赖 opencodeomo 这种可选插件模块（那会把
// runtime/takeover 的测试和 cli → runtime/takeover 的生产依赖拼成环，
// 2026-09-17 实测触发）。
type fakeConfigTakeover struct{}

func (fakeConfigTakeover) Targets() []string       { return nil }
func (fakeConfigTakeover) IsTakenOver(string) bool { return false }
func (fakeConfigTakeover) Apply(int) ([]*agentapi.TakeoverReport, error) {
	return nil, nil
}
func (fakeConfigTakeover) Restore() ([]string, error) { return nil, nil }

func TestMain(m *testing.M) {
	claude := claudecode.Agent()
	open := opencode.Agent()
	open.Config = fakeConfigTakeover{}
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
