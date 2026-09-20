package app

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/ciyaml"
)

// TestWorkflowsAreStructurallyValid 是 `tools/ciyaml` 的**双保险**：CI 是唯一
// 能跑全部测试的地方，而它自己就是被检查的那份文件——workflow 一旦非法，GitHub
// 0 秒拒掉整个 run，一个 job 都不起。所以这条判据不能只住在 CI 里（那就成了
// 「消防队住在着火的房子里」），必须能在本地 `go test ./...` 就把人拦住。
//
// 2026-09-20 的教训：摊平 go/ 那次删掉了一个步骤的 `run:` 行，接下来六个提交的
// CI 全红，看着像测试挂了，实际是测试根本没跑。判据在 tools/ciyaml（实现一份），
// 发行版那条同名棘轮（testing/ci_test.go）用的是同一个包。
func TestWorkflowsAreStructurallyValid(t *testing.T) {
	root := filepath.Join(repoDir(t), "..")
	findings, err := ciyaml.Check(root)
	if err != nil {
		t.Fatalf("CI 配置检查跑不起来——判据退化了（workflow 目录挪了？）: %v", err)
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
}
