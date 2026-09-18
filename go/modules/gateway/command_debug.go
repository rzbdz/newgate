package gateway

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate/go/lib/durarg"
	"github.com/rzbdz/newgate/go/lib/style"
	cliapi "github.com/rzbdz/newgate/go/modules/cli/extension"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/gateway/gatewaystate"
)

const debugDefaultMinutes = 30

// debugCommand 是 `newgate debug on|off [分钟] [--forever]`：全量请求日志。
//
// **它住在这里而不是 modules/cli**：被打开的是网关转发侧逐条 dump 请求与响应
// （见 forward 里的 dump 闸门），所以「有没有开」是 gateway 的语义。搬过来之后
// cli 的 switch 与 --help 都不再需要认识这个动词——理由与 specialCommand 同一条。
//
// 默认**限时**：一发请求的 body 可以到 8KB 以上（opencode 的 system prompt
// 单独就 ~97KB），忘了关会把磁盘写满。
type debugCommand struct{}

var (
	_ cliapi.Command    = (*debugCommand)(nil)
	_ cliapi.Documented = (*debugCommand)(nil)
)

func (debugCommand) Names() []string { return []string{"debug"} }

func (debugCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage:   "debug on|off [分钟]",
		Summary: "全量请求日志（默认 30 分钟自动关）",
	}
}

func (debugCommand) Run(host cliapi.Host, args []string) int {
	arg := cliapi.Arg(args, 0)
	// 同 schema-repair：不带参数要报用法，不能默认成 off。这条命令的代价更大
	// ——它会在你只想看一眼的时候把全量日志关掉，而「日志怎么没了」的排查
	// 成本远高于多敲一个 on/off。
	switch arg {
	case "":
		return host.Die(64, "用法：newgate debug on [分钟] [--forever] | off（不带参数不改任何东西）")
	case "on", "off":
	default:
		// `newgate debug 30` 是「开 30 分钟」的简写，保留。
		if _, err := durarg.Parse(arg); err != nil {
			return host.Die(64, fmt.Sprintf("debug: 不认识的参数 %q（on / off / 分钟数，如 30）", arg))
		}
	}
	on := arg != "off"
	ttl := debugDefaultMinutes * time.Minute
	if on {
		for _, a := range args {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				ttl = time.Duration(n) * time.Minute
			}
		}
		if hasFlag(args, "--forever") {
			ttl = 0
		}
	}

	until := ""
	if ttl > 0 {
		until = time.Now().Add(ttl).Format(time.RFC3339)
	}
	if err := gatewaystate.SetDebug(on, until); err != nil {
		return host.Die(70, err.Error())
	}

	if !on {
		fmt.Println(style.Item(style.Skip, "debug off"))
		return 0
	}
	if ttl > 0 {
		fmt.Println(style.Item(style.OK, fmt.Sprintf("debug on · %s 后自动关闭",
			durarg.Format(int(ttl.Seconds())))))
		fmt.Println(style.Hint("不设期限：newgate debug on --forever"))
	} else {
		fmt.Println(style.Item(style.Warn, "debug on · 不自动关闭"))
		fmt.Println(style.Hint("记得 newgate debug off"))
	}
	fmt.Println(style.Hint("日志 " + paths.LogFile() + " · 16MB 轮转，保留 4 份"))
	host.NotifyProxy()
	if host.DaemonRunning() {
		fmt.Println(style.Hint("即刻生效，无需重启"))
	}
	return 0
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}
