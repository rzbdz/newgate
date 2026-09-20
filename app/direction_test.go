package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 组合根**不认识任何模块**——这条法则今天是一条测试，不是一句注释。
//
// 判据是机械的：`app/*.go` 的非测试源码里不许出现任何
// `github.com/rzbdz/newgate/modules/...` 的 import，除了两处**不是依赖**的：
//
//   - `modules_gen.go`：构建期由 tools/genmodules 生成的装配清单。清单必须住在
//     某个地方，而组合根是它唯一该在的地方（它引用的每个模块都只被列一次名，
//     不调用任何方法）。
//   - `modules/config/paths`：**共享叶子**，谁都能直接 import（docs/03 的分层表
//     写明），它描述的是「配置在磁盘上长什么样」，不是「某个模块怎么工作」。
//     这里用它决定装配日志写到哪个文件。
//
// # 为什么值得钉住
//
// 破了它，症状是**编译期才出现，而且是别人踩的**：用户从清单里摘掉一个模块
// （那正是「装一个模块 = 复制目录」的另一半），组合根却因为它 import 了某个具体
// 模块而编译不过。这一条与「模块可摘」是同一条不变式的两面——见 matrix_test.go
// 的摘除矩阵。
//
// 2026-09-20 之前这里违反过三处，全是「组合根替模块做决定」：
// `MustGet(pluginmanager).SetCatalog(...)`（名单现在由内核递，见 component.CatalogAware）、
// `App.CLI()/Wrapper()`（入口现在由模块自己申报，见 component/entry）、
// `Agent/Slot` 类型别名（零调用方，纯冗余）。
func TestTheCompositionRootNamesNoModule(t *testing.T) {
	root := repoDir(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读不了 %s: %v", root, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// 生成物整份跳过：它列的就是全部模块名，那是**构建期扫描的结果**，
		// 与「组合根认识某个模块」是两回事（它一个方法都不调）。
		if name == "modules_gen.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("读不了 %s: %v", name, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, `"github.com/rzbdz/newgate/modules/`) {
				continue
			}
			// 唯一的例外是共享叶子 config/paths（docs/03 的分层表写明谁都能
			// 直接 import）：它描述「配置在磁盘上长什么样」，不是某个模块的内部。
			if strings.Contains(line, "modules/config/paths") {
				continue
			}
			t.Errorf("app/%s:%d 组合根 import 了具体模块——"+
				"摘掉那个模块，组合根就编译不过了：\n    %s\n"+
				"    组合根该做的：装图、把这次调用交给入口。模块自己的知识走\n"+
				"    component/entry（谁是入口）与 component.CatalogAware（图里有谁）。",
				name, i+1, strings.TrimSpace(line))
		}
	}
}

// repoDir 是 go/ 目录（本测试所在包的上两级）。
func repoDir(t *testing.T) string {
	t.Helper()
	_, this, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 拿不到本文件路径")
	}
	return filepath.Dir(this)
}
