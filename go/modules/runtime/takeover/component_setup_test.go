package takeover

import (
	"os"
	"sort"
	"testing"

	"github.com/rzbdz/newgate/go/modules/claudecode"
	"github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/opencode"
	"github.com/rzbdz/newgate/go/modules/opencodeomo"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
)

type testCatalog map[string]*api.Agent

func (catalog testCatalog) Get(id string) (*api.Agent, bool) {
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

func (testCatalog) StateFieldOwner(string) (string, bool) { return "", false }

func TestMain(m *testing.M) {
	claude := claudecode.Agent()
	open := opencode.Agent()
	open.Config = opencodeomo.Takeover()
	restore := agentstate.Set(testCatalog{claude.ID: claude, open.ID: open})
	code := m.Run()
	restore()
	os.Exit(code)
}
