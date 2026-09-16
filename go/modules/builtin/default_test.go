package builtin

import (
	"reflect"
	"testing"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

func TestDefaultGraphStartsWithConfigGatewayAndConfigHook(t *testing.T) {
	names := manager.ComponentNames()
	if len(names) < 3 ||
		names[0] != "config" ||
		names[1] != "gateway" ||
		names[2] != "config-hook" {
		t.Fatalf("component order = %v", names)
	}
	if got, want := Names(), []string{"claude", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
	opencode, ok := Get("opencode")
	if !ok || opencode.Config == nil {
		t.Fatal("opencode takeover was not injected by opencode-omo")
	}
}

func TestGatewayReceivesModuleHooks(t *testing.T) {
	var names []string
	for _, plugin := range special.Plugins() {
		names = append(names, plugin.Name())
	}
	for _, want := range []string{
		"claude-bg", "claudecode-deepseek", "deepseek", "glm", "always-thinks",
	} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("gateway hooks = %v, missing %s", names, want)
		}
	}
}
