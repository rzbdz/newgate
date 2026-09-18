package app

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	modscan "github.com/rzbdz/newgate/go/tools/genmodules/scan"
)

// TestGeneratedModuleListIsCurrent 拦住「往 modules/ 加了目录但忘了重新生成」
// 这类漂移：清单过期时 `make build` 会自动重生成，但直接 `go build`/`go test`
// 的人不会经过 Makefile，而那时的症状是模块在仓库里却根本没被装配——静默的
// 功能缺失，比编译错误难查得多，所以在这里显式失败一次。
func TestGeneratedModuleListIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("需要跑子进程重新生成，-short 下跳过")
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(filepath.Dir(current)) // go/app/ → go/

	cmd := exec.Command("go", "run", "./tools/genmodules", "-check")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("装配清单已过期，请运行 `make generate`：\n%s", output)
	}

	// 双保险：-check 是拿生成器和自己的输出比，若生成器逻辑本身退化成空，
	// 它会自我一致。所以这里再断言清单非空且覆盖 modules/ 下的每个目录。
	generated := generatedComponents()
	if len(generated) == 0 {
		t.Fatal("装配清单为空：生成器没有扫到任何组件")
	}
	dirs := moduleDirs(t, filepath.Join(root, "modules"))
	if len(generated) != len(dirs) {
		t.Fatalf("装配了 %d 个组件，modules/ 下有 %d 个目录", len(generated), len(dirs))
	}
}

// moduleDirs 返回 modules/ 下所有「是组件」的目录名。
//
// 用的就是**生成器那把尺子**（tools/genmodules/scan）：判据曾经有两份、口径还
// 不同（这里用 bytes.Contains 找字面量，生成器用 AST 看返回值），于是照文档写
// `func New() component.Component` 的模块会被生成器装进清单、却数不进这里，
// 报出一个完全指不到真因的失败（2026-09-18 实测，见 scan 的包注释）。
func moduleDirs(t *testing.T, modulesDir string) []string {
	t.Helper()
	dirs, err := modscan.Dirs(modulesDir)
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}
