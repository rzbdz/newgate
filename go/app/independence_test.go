package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 内核不带任何发行版机制——这条边界今天是一条测试，不是一句注释。
//
// 2026-09-20 之前内核里有一整套「装哪个发行版」的机制：仓库根的 modules-ext.json
// （一张 Pin）、tools/extmanifest（按 Pin 去 clone 发行版仓库）、genmodules 的
// -ext 那一半。它换来的代价实测有三条，每条都咬过人：
//
//   - 编一次发行版就把**内核 checkout 改脏**（生成的装配清单被重写成发行版那份），
//     于是 `git -C core pull` / 换分支被工作区挡住；
//   - 内核 CI 必须去 `git clone` 另一个仓库，内核的构建不再离线；
//   - 清单里 import 了 modules-ext 下的包 ⇒ **核心里出现了一条指向发行版的编译期
//     依赖**，而「内核不依赖发行版」正是这套分层的定义。
//
// 现在发行版是另一个 Go module，把自己那张表交给 app.Selection（见 manifest.go）。
// 这条测试钉的就是「那套东西不许回来」——它管的是机制，不是文字：解释这段历史的
// 注释可以留着（那些注释正是在防止下一个人重新发明它）。
func TestKernelCarriesNoDistributionMachinery(t *testing.T) {
	goDir := filepath.Join(repoDir(t), "..")
	repoRoot := filepath.Dir(goDir)

	// 1. Pin 文件本身：它写的是「本构建用哪个发行版」——那是产品决定。
	pin := filepath.Join(repoRoot, "modules-ext.json")
	if _, err := os.Stat(pin); err == nil {
		t.Errorf("%s 又出现了。内核不该声明「用哪个发行版」——装哪些模块是发行版自己的事，"+
			"它拿 app.Selection 把表交给组合根；Pin 属于发行版仓库", pin)
	}

	// 2. 代码里的机制：import 路径、包名、两个环境变量。
	//
	// 只查这三类**代码形态**，不查光秃秃的 "modules-ext" 字样：模块 checkout 的
	// 目录名会出现在讲这段历史的注释里，那是资产不是负债。
	forbidden := []struct{ needle, why string }{
		{`"github.com/rzbdz/newgate/go/modules-ext`, "发行版模块的 import 路径——核心里出现它，说明又在构建期把发行版编进来了"},
		{"extmanifest.", "按 Pin 拉发行版仓库的那套工具"},
		{"NEWGATE_MODULES_PIN", "覆盖 Pin 的环境变量"},
		{"NEWGATE_EXT_DRYRUN", "关掉 clone 的环境变量"},
	}
	err := filepath.WalkDir(goDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 构建产物目录（老机制留下的）里是别人的代码，不是内核的。
			if d.Name() == "modules-ext" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, p)
		for _, f := range forbidden {
			if strings.Contains(string(body), f.needle) {
				t.Errorf("%s 里出现了 %q：%s", rel, f.needle, f.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s：%v", goDir, err)
	}
}
