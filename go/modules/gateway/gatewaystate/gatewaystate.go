// Package gatewaystate 是 gateway 在 state.json 里自己那一段。
//
// 为什么单开一个叶子包（2026-09-18）：这三个开关（schema 修补、special 层总开关、
// 单插件开关）原来声明成 `domain.State` 上的 typed 字段。那等于把**网关的开关词汇
// 写进了共享配置**——同一个 struct 里躺着所有模块的键，加一个网关开关就要动 config。
// 现在它们住在 `state.json` 的 `ModuleConfig["gateway"]` 里，与
// `modules/claudecode` 的 `classifier_naked`、`modules/pluginmanager` 的
// `plugin_manager` 完全同款：**谁的状态谁自己解析，core 不认识任何具体键**。
//
// 为什么是叶子：`gateway/special`（插件层）和 `gateway/forward`（转发热路径）都要
// 读它，而 forward 不能反向 import gateway 根包。放在这里，两个方向都安全——
// 与 modules/gateway/controlpath 同一条规矩。
package gatewaystate

import (
	"encoding/json"

	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/store"
)

// Key 是 state.json 的 ModuleConfig 里属于 gateway 的字段名。
const Key = "gateway"

// Config 是 gateway 的运行期开关。
//
// 三个字段都是「可缺席」的：SchemaRepair / SpecialTreatment 用指针以区分
// 「没配」和「显式关闭」，SpecialOff 空切片与 nil 等价。缺席 = 出厂态（开）。
type Config struct {
	SchemaRepair *bool `json:"schema_repair,omitempty"`
	// SpecialTreatment special_treatment 插件层总开关（默认开）。
	// 插件只对认领的上游生效（gateway/special），所以开着不影响别人。
	SpecialTreatment *bool `json:"special_treatment,omitempty"`
	// SpecialOff 单独关掉的插件名。排查「是不是 newgate 改坏了请求」时
	// 关掉某一个比关掉整层更精确。名字见 `newgate st`。
	SpecialOff []string `json:"special_treatment_off,omitempty"`
}

// legacy 是迁移前的老键名：这三个开关曾经声明成 domain.State 上的 typed 字段，
// 存在 state.json 的**顶层**。2026-09-18 起改由本模块自己托管。
//
// 为什么必须读老键：store 对「顶层不认识的键」是**无损保留**的（见 store.LoadState
// / SaveState），所以老键既不会自动消失、也不会被新的读路径看到。不读它，一个
// 显式 `special_treatment: false` 过的机器在升级后会**静默变回全开**——用户关掉的
// 东西自己回来了，而且没有任何提示。
var legacy = struct{ SchemaRepair, SpecialTreatment, SpecialOff string }{
	"schema_repair", "special_treatment", "special_treatment_off",
}

// Parse 解析 gateway 那一段。**永不失败**：没有这一段、或者 JSON 坏掉，
// 都当「出厂态」——全开、没有单独关掉的插件（fail-open：读不出来就别关人东西）。
//
// 新段缺席时读一次老键（迁移期）。一旦新段存在（哪怕是 `{}`），就只认它——
// 否则用户把开关都拨回出厂态之后，老键会把旧值复活。
func Parse(st *domain.State) Config {
	if st == nil {
		return Config{}
	}
	var c Config
	if raw := st.ModuleConfig[Key]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &c)
		return c
	}
	return legacyConfig(st)
}

func legacyConfig(st *domain.State) Config {
	var c Config
	if raw := st.ModuleConfig[legacy.SchemaRepair]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.SchemaRepair = &v
		}
	}
	if raw := st.ModuleConfig[legacy.SpecialTreatment]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.SpecialTreatment = &v
		}
	}
	if raw := st.ModuleConfig[legacy.SpecialOff]; len(raw) > 0 {
		var v []string
		if json.Unmarshal(raw, &v) == nil {
			c.SpecialOff = v
		}
	}
	return c
}

// Marshal 编码成要写进 ModuleConfig 的字节。
func (c Config) Marshal() ([]byte, error) { return json.Marshal(c) }

// 下面三个是**读侧**的便捷函数，直接收 `*domain.State`：调用点遍布热路径，
// 让它们各自 Parse 一次比要求每个调用方先解好再传进来清楚得多（小 JSON，
// 与 claudecode 每请求调 ParseNakedConfig 同一量级）。

// RepairEnabled 给缺 required 的 tool schema 补 "required": []。
// 按 JSON Schema 规范这是语义无操作，所以默认开。
func RepairEnabled(st *domain.State) bool {
	return Parse(st).repairOn()
}

// SpecialEnabled special_treatment 层是否启用。默认开。
func SpecialEnabled(st *domain.State) bool {
	return Parse(st).specialOn()
}

// PluginOff 某个插件是否被单独关掉。
func PluginOff(st *domain.State, name string) bool {
	return Parse(st).pluginOff(name)
}

func (c Config) repairOn() bool  { return c.SchemaRepair == nil || *c.SchemaRepair }
func (c Config) specialOn() bool { return c.SpecialTreatment == nil || *c.SpecialTreatment }

func (c Config) pluginOff(name string) bool {
	for _, n := range c.SpecialOff {
		if n == name {
			return true
		}
	}
	return false
}

// ---------- 写侧 ----------

// SetSchemaRepair 开关 schema 修补。
func SetSchemaRepair(on bool) error {
	return update(func(c *Config) { c.SchemaRepair = &on })
}

// SetSpecialTreatment 开关整个 special_treatment 层。
func SetSpecialTreatment(on bool) error {
	return update(func(c *Config) { c.SpecialTreatment = &on })
}

// SetSpecialPlugin 单独开关一个插件。关 = 记进 SpecialOff，开 = 从里面删掉。
func SetSpecialPlugin(name string, on bool) error {
	return update(func(c *Config) {
		out := make([]string, 0, len(c.SpecialOff))
		for _, n := range c.SpecialOff {
			if n != name {
				out = append(out, n)
			}
		}
		if !on {
			out = append(out, name)
		}
		if len(out) == 0 {
			out = nil
		}
		c.SpecialOff = out
	})
}

// update 读一份 state、改 gateway 那一段、写回去。读-改-写整体一次，
// 免得两个 setter 交叉覆盖（原来在 store 里也是这个形状）。
func update(mutate func(*Config)) error {
	st := store.LoadState()
	c := Parse(st)
	mutate(&c)
	raw, err := c.Marshal()
	if err != nil {
		return err
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[Key] = raw
	// 老键显式删掉。store 的无损保留意味着不删就永远躺在 state.json 里，
	// 下一个人看到会以为它还在生效（实测：迁移后顶层仍留着 schema_repair: true）。
	// 新值已经落在 Key 那一段里，删掉不会丢信息。
	delete(st.ModuleConfig, legacy.SchemaRepair)
	delete(st.ModuleConfig, legacy.SpecialTreatment)
	delete(st.ModuleConfig, legacy.SpecialOff)
	return store.SaveState(st)
}
