package ciyaml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write 造一个最小仓库：只有 .github/workflows/<name>。
func write(t *testing.T, name, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	return root
}

// good 是一份**结构上合法**的 workflow，两种步骤写法都覆盖：
// 破折号那一行直接 `uses:`，以及 `- name:` + 缩进一级的 `run:`。
const good = `name: CI
on:
  push:
    branches: [main]

jobs:
  static:
    name: 静态检查
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: go
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: 单测
        run: GOPROXY=off go test ./...
`

func TestAcceptsAValidWorkflow(t *testing.T) {
	got, err := Check(write(t, "ci.yml", good))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(got) != 0 {
		for _, f := range got {
			t.Errorf("合法文件被误报: %s", f)
		}
	}
}

// TestCatchesTheStepThatTookDownCI 就是 2026-09-20 那次事故的复现：摊平 go/ 时
// 一个步骤的 `run:` 行被连带删掉，GitHub 从此 0 秒拒掉整个 run。判据必须抓住它。
func TestCatchesTheStepThatTookDownCI(t *testing.T) {
	const broken = `name: CI
on: push

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: opencode 侧端到端

      - name: Claude Code 侧端到端
        run: bash mock/e2e_claude.sh
`
	got, err := Check(write(t, "ci.yml", broken))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "既没有 run 也没有 uses")
	if !strings.Contains(got[0].String(), "opencode 侧端到端") {
		t.Errorf("报的应该是那一步，实际：%s", got[0])
	}
	if got[0].Line != 10 {
		t.Errorf("行号 = %d，想要 10：%s", got[0].Line, got[0])
	}
}

// TestCatchesEmptyValues 覆盖事故的另一半——被删空的 `defaults: run:`。
func TestCatchesEmptyValues(t *testing.T) {
	const broken = `name: CI
on: push

jobs:
  static:
    runs-on: ubuntu-latest
    defaults:
      run:
    steps:
      - name: 构建
        run:
`
	got, err := Check(write(t, "ci.yml", broken))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	// 两处都要报：`defaults:` 下的那个（事故原样）和步骤里那个。少报一处就说明
	// 判据只认了位置、没认形态。
	if len(got) != 2 {
		t.Fatalf("两处空值都该报，实际 %d 条: %v", len(got), got)
	}
	for _, f := range got {
		if !strings.Contains(f.String(), "空值") {
			t.Errorf("报的不是空值: %s", f)
		}
	}
}

func TestCatchesMissingRunsOnAndSteps(t *testing.T) {
	const broken = `name: CI
on: push

jobs:
  static:
    name: 没有 runs-on
  e2e:
    runs-on: ubuntu-latest
`
	got, err := Check(write(t, "ci.yml", broken))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "没有 runs-on", "static")
	assertOne(t, got, "没有 steps", "e2e")
}

func TestCatchesNoJobsAndTabs(t *testing.T) {
	got, err := Check(write(t, "ci.yml", "name: CI\non: push\n"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "没有 jobs")

	tabbed := "name: CI\non: push\n\njobs:\n\tstatic:\n    runs-on: ubuntu-latest\n"
	got, err = Check(write(t, "ci.yml", tabbed))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "tab")
}

// TestDegeneratesLoudly 判据自己坏掉时（目录挪了、没有 workflow）必须**报错**，
// 不能返回「零条问题」——那和「查过了，很干净」长得一模一样。
func TestDegeneratesLoudly(t *testing.T) {
	if _, err := Check(t.TempDir()); err == nil {
		t.Fatal("没有 .github/workflows 时应该报错，而不是安静地返回零条")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(root); err == nil {
		t.Fatal("目录是空的时应该报错，而不是安静地返回零条")
	}
}

// assertOne 断言恰好有一条 finding 同时包含全部片段——顺带保证不会一条报成八条。
func assertOne(t *testing.T, got []Finding, fragments ...string) {
	t.Helper()
	hits := 0
	for _, f := range got {
		all := true
		for _, fr := range fragments {
			if !strings.Contains(f.String(), fr) {
				all = false
			}
		}
		if all {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("想要恰好一条含 %q 的 finding，实际 %d 条：%v", fragments, hits, got)
	}
}
