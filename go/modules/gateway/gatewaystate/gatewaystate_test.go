package gatewaystate

import (
	"encoding/json"
	"testing"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/store"
	"github.com/rzbdz/newgate/go/testing/testkit"
)

func st(m map[string][]byte) *domain.State {
	return &domain.State{ModuleConfig: m}
}

func raw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseFallsBackToFactory 没有这一段、或者 JSON 坏掉，都当出厂态。
// 方向是 fail-open：读不出来**别关人东西**（关了会让 DeepSeek 那边开始回 400）。
func TestParseFallsBackToFactory(t *testing.T) {
	for _, c := range []struct {
		name string
		st   *domain.State
	}{
		{"nil state", nil},
		{"空 ModuleConfig", st(nil)},
		{"这一段的 JSON 是坏的", st(map[string][]byte{Key: []byte("{不是 json")})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !RepairEnabled(c.st) {
				t.Fatal("schema 修补该默认开")
			}
			if !SpecialEnabled(c.st) {
				t.Fatal("special 层该默认开")
			}
			if PluginOff(c.st, "deepseek") {
				t.Fatal("没配过就不该有插件被关")
			}
		})
	}
}

// TestParseReadsLegacyKeys 老文件（顶层 typed 字段）必须还能读出来。
//
// 这条守的是一次**静默的**行为反转：store 对顶层不认识的键是无损保留的，老键
// 既不会自动消失、也不会被新读路径看到。不读它，一台显式 `special_treatment:
// false` 过的机器升级后会自己变回全开——用户关掉的东西悄悄回来了。
func TestParseReadsLegacyKeys(t *testing.T) {
	off := false
	old := st(map[string][]byte{
		legacy.SchemaRepair:     raw(t, false),
		legacy.SpecialTreatment: raw(t, off),
		legacy.SpecialOff:       raw(t, []string{"deepseek", "glm"}),
	})
	if RepairEnabled(old) {
		t.Fatal("老键 schema_repair=false 没被读到")
	}
	if SpecialEnabled(old) {
		t.Fatal("老键 special_treatment=false 没被读到")
	}
	if !PluginOff(old, "deepseek") || !PluginOff(old, "glm") {
		t.Fatal("老键 special_treatment_off 没被读到")
	}
	if PluginOff(old, "thinking") {
		t.Fatal("没关过的插件被误判成关了")
	}
}

// TestNewSectionWins 新段一旦存在（哪怕只是个 `{}`），就只认它。
//
// 否则用户把开关都拨回出厂态之后，老键会把旧值复活——那正是这个迁移最容易
// 留下的坑：数据看着是对的，行为却在两种来源之间反复横跳。
func TestNewSectionWins(t *testing.T) {
	s := st(map[string][]byte{
		Key:                     []byte("{}"),
		legacy.SpecialTreatment: raw(t, false),
	})
	if !SpecialEnabled(s) {
		t.Fatal("新段存在时不该再读老键（老值把开关复活了）")
	}
}

// TestUpdateWritesNewAndDropsLegacy 写侧：值落到新段、老键当场删掉。
func TestUpdateWritesNewAndDropsLegacy(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 先造一份带老键的 state。
	s := store.LoadState()
	s.ModuleConfig[legacy.SpecialTreatment] = raw(t, false)
	s.ModuleConfig[legacy.SchemaRepair] = raw(t, true)
	if err := store.SaveState(s); err != nil {
		t.Fatal(err)
	}
	// 迁移期读得到老值。
	if SpecialEnabled(store.LoadState()) {
		t.Fatal("写之前该读到老键的 false")
	}

	if err := SetSpecialPlugin("deepseek", false); err != nil {
		t.Fatal(err)
	}

	after := store.LoadState()
	if _, still := after.ModuleConfig[legacy.SpecialTreatment]; still {
		t.Fatal("老键没被删掉——它会永远躺在 state.json 里让人以为还生效")
	}
	if _, still := after.ModuleConfig[legacy.SchemaRepair]; still {
		t.Fatal("老键 schema_repair 没被删掉")
	}
	if PluginOff(after, "deepseek") != true {
		t.Fatal("新段没写进去")
	}
	// 老值被带进了新段：写的那一刻先读到了 false，所以它必须活下来。
	if SpecialEnabled(after) {
		t.Fatal("老键的 false 在迁移时被丢了——用户关掉的东西自己回来了")
	}
}
