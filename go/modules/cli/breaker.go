package cli

import (
	"fmt"
	"time"

	breakerapi "github.com/rzbdz/newgate/go/modules/breaker"
	"github.com/rzbdz/newgate/go/modules/cli/style"
)

// cmdBreaker 只回答一件事：**现在哪些 binding 被摘牌了、为什么**。
//
// 为什么单独立一条命令，不并进 `metrics`：`metrics` 的「模型健康」段落
// （printModelHealth）故意把卡顿/不可用的详情藏起来只说个数量，理由写在
// 那段注释里——避免体检退化成日志墙。但排查「deepseek 怎么又被摘了」这
// 类问题时，恰恰要看那几条被藏起来的详情：什么时候打开的、打开了多久、
// 连续失败几次、上一次 probe 给的评级与延迟。这条命令就是把这些字段摊开。
//
// 2026-09-17 加：reasoning-400 这类请求形状错误曾经被误记进熔断器连续两次
// 就摘牌，probe 却一直绿——用户看到「deepseek 明明能用却被摘了」查不到
// 原因。修复后这类 400 不再计入 `fails`，但排查历史现场仍然要能看见
// 「现在到底谁被摘了、原因是不是可信」，所以留着这条命令。
func cmdBreaker() int {
	info, ps := proxyState()
	if info == nil {
		return die(69, "代理没在运行（newgate start）——熔断表在 daemon 内存里")
	}
	if ps == nil {
		return die(69, fmt.Sprintf("连不上代理 127.0.0.1:%d（newgate doctor）", info.Port))
	}

	var open []breakerapi.Status
	for _, b := range ps.Breakers {
		if b.Open {
			open = append(open, b)
		}
	}

	if len(open) == 0 {
		fmt.Println(style.Title("newgate breaker", fmt.Sprintf("pid %d", info.PID)))
		fmt.Println(style.Hint("没有被摘牌的 binding"))
		return 0
	}

	fmt.Println(style.Title("newgate breaker",
		fmt.Sprintf("pid %d · %d 个被摘牌", info.PID, len(open))))

	t := style.NewTable("binding", "打开了多久", "连续失败", "原因", "上次 probe")
	for _, b := range open {
		age := "-"
		if b.OpenFor > 0 {
			age = prettyDur(int(time.Duration(b.OpenFor).Seconds()))
		}
		probeInfo := "未探活"
		if !b.Checked.IsZero() {
			probeInfo = fmt.Sprintf("%s @ %dms（%s 前）",
				b.Grade, b.Latency, time.Since(b.Checked).Round(time.Second))
		}
		t.Row(b.Provider+"/"+b.Model, age, fmt.Sprintf("%d", b.Fails), b.Reason, probeInfo)
	}
	fmt.Print(t.String())

	fmt.Println()
	fmt.Println(style.Hint(
		"解封只能靠 probe 证明恢复（RecordSuccess 不解封，见 docs/05-gateway.md §3）："))
	fmt.Println(style.Hint("  newgate probe        # 重新探活，健康的会在冷却期后自动解封"))
	fmt.Println(style.Hint("  newgate tier <档位>   # 看这个 binding 在链里排第几、是不是被跳过"))
	return 0
}
