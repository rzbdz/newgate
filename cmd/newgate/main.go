// Command newgate 是**内核自带**的二进制：进程组合根 + 内核 `modules/` 下的全部
// 模块（现在是十一个），一个外部模块都不装。
//
// 它存在的理由是「内核自己可以被构建、被测试」——`make e2e` 需要**一个**二进制，
// 内核的单元/系统测试也需要一张能起来的真图。**它不是产品**：产品的模块集合由
// 发行版决定（发行版是另一个 Go module，见 docs/09-extension-guide.md §8），
// 所以别把这里的产物装到线上——那是个没有上游怪癖补丁的降级版。
//
// 组合根的逻辑（留痕 → 起图 → 问入口 → 交出去）全在 app.Main 里，本文件只负责
// 说「哪张图、什么版本」。发行版的 main 也只是同样十几行，两份不会漂移。
package main

import (
	"context"
	"os"

	app "github.com/rzbdz/newgate/app"
)

func main() {
	os.Exit(app.Main(context.Background(), app.Options{
		Version:    version,
		BuildTime:  buildTime,
		CommitTime: commitTime,
	}))
}
