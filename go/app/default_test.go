package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
)

// TestDefaultGraphStartsWithConfigGatewayAndConfigHook 锁住「地基先起」：
// config / config-hook / gateway 三个提供者必须排在最前。
//
// 只断言这三个的**集合**，不锁它们的相对顺序：装配清单现在是构建期扫描
// modules/ 按字母序生成的（app/modules_gen.go），三者互相独立、拓扑排序
// 遇到并列时按声明位置决胜，相对顺序因此是生成顺序的副产品，不是设计意图。
// 真正有意义的约束是「它们在任何消费者之前」，那由拓扑排序保证。
func TestDefaultGraphStartsWithConfigGatewayAndConfigHook(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	names := app.ComponentNames()
	if len(names) < 3 {
		t.Fatalf("component order = %v", names)
	}
	foundation := map[string]bool{"config": false, "config-hook": false, "gateway": false}
	for _, name := range names[:3] {
		if _, ok := foundation[name]; !ok {
			t.Fatalf("component order = %v; 前三个应是 config/config-hook/gateway", names)
		}
		foundation[name] = true
	}
	for name, seen := range foundation {
		if !seen {
			t.Fatalf("component order = %v; 缺少 %s", names, name)
		}
	}
	if got, want := app.Names(), []string{"claude", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
	opencode, ok := app.Get("opencode")
	if !ok || opencode.Config == nil {
		t.Fatal("opencode takeover was not injected by opencode-omo")
	}
}

func TestGatewayReceivesModuleHooks(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
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
