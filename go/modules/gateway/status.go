package gateway

import (
	"strings"

	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/gateway/gatewaystate"
)

// switchStatus 把本模块的三个补丁开关报给 `newgate status`。
//
// **为什么由 gateway 自己报、而不是让 cli 读 state**：这三个键现在住在
// state.json 的 ModuleConfig["gateway"] 里（见 gatewaystate 的包说明），
// 是 gateway 自己的词汇；cli 若为了这一行去解析它们，就等于换了个地方
// 继续认识 gateway 的内部结构。谁的状态谁自己报——与 plugin-manager 报
// 它那份开关账本是同一个口（RegisterStatus）。
//
// 只列**离开出厂态**的：三个开关全都默认开，一行「全开」不占版面也没信息量，
// 与 status 那一屏「一眼看有没有被关掉东西」的定位一致。
type switchStatus struct{}

var _ cliapi.StatusProvider = switchStatus{}

// Status 见类型说明。st 为 nil 时什么都不报（快照还没装上）。
func (switchStatus) Status(st *domain.State) []cliapi.StatusLine {
	if st == nil {
		return nil
	}
	var parts []string
	if !gatewaystate.RepairEnabled(st) {
		parts = append(parts, "schema-repair=off")
	}
	switch {
	case !gatewaystate.SpecialEnabled(st):
		parts = append(parts, "special_treatment=off")
	case len(gatewaystate.Parse(st).SpecialOff) > 0:
		parts = append(parts, "special 关了 "+strings.Join(gatewaystate.Parse(st).SpecialOff, ","))
	}
	if len(parts) == 0 {
		return nil
	}
	return []cliapi.StatusLine{{
		Label: "补丁开关",
		Value: strings.Join(parts, "   ") + "   恢复：newgate st on · newgate schema-repair on",
	}}
}
