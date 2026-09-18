package gateway

import (
	"fmt"

	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/gateway/gatewaystate"
)

// schemaRepairCommand 是 `newgate schema-repair on|off`。
//
// **它住在这里而不是 modules/cli**：工具 schema 的修补是网关数据面的一步
// （`schema.Repair`，跑在转发热路径上），所以「有没有开」是 gateway 的语义，
// 命令也该由 gateway 提供。理由与 specialCommand 同一条。
//
// 为什么值得有这个命令：这一层会**改用户的请求**。某些上游对工具 schema 的
// `required` 缺省极敏感，补一个空数组能让请求过去；但排查「是不是 newgate 改坏的」
// 时必须能一眼关掉它。
type schemaRepairCommand struct{}

var (
	_ cliapi.Command    = (*schemaRepairCommand)(nil)
	_ cliapi.Documented = (*schemaRepairCommand)(nil)
)

func (schemaRepairCommand) Names() []string { return []string{"schema-repair", "schema_repair"} }

func (schemaRepairCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: "维护",
		Usage:   "schema-repair on|off",
		Summary: "工具 schema 缺 required 时补空数组",
	}
}

func (schemaRepairCommand) Run(host cliapi.Host, args []string) int {
	on := truthy(cliapi.Arg(args, 0))
	if err := gatewaystate.SetSchemaRepair(on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, "schema repair on") +
			style.Dim("   工具 schema 缺 required 时补一个空数组"))
	} else {
		fmt.Println(style.Item(style.Skip, "schema repair off") +
			style.Dim("   工具 schema 原样转发"))
	}
	host.NotifyProxy()
	return 0
}

// truthy 认得 on / 1 / true / yes。与 CLI 那侧同一个定义——命令搬过来了，
// 判据也得跟着搬，否则 `newgate schema-repair 1` 会静默变成「关」。
func truthy(s string) bool { return s == "on" || s == "1" || s == "true" || s == "yes" }
