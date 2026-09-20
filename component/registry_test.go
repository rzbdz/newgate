package component

import (
	"errors"
	"strings"
	"testing"
)

// Registry 的三条不变量：Release 精确撤销自己那一条、陈旧 Release 无害、
// check 拒绝时账本不变。

func TestRegistryReleaseRemovesOnlyItsOwnEntry(t *testing.T) {
	var r Registry[string]

	first, err := r.Register("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", nil); err != nil {
		t.Fatal(err)
	}

	if err := first(); err != nil {
		t.Fatalf("撤销报错: %v", err)
	}
	if got := r.All(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("撤销 a 之后账本 = %v, want [b]", got)
	}
}

// 陈旧 Release 返回 nil 而不是报错，是为了让组件的 Stop 幂等。
// 组件的 Stop 可能被调用不止一次（装配失败回滚 + 正常停止），释放路径上
// 多一次「已经没了」不该被当成故障。
func TestRegistryStaleReleaseIsNoop(t *testing.T) {
	var r Registry[string]

	release, err := r.Register("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", nil); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	if err := release(); err != nil {
		t.Errorf("重复撤销应当是无害的 no-op，实际报错: %v", err)
	}
	if got := r.All(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("陈旧 Release 动了别人的条目: %v", got)
	}
}

// check 在写锁内跑，所以「查重 + 插入」是原子的；被拒绝时账本一个字节都不动。
func TestRegistryCheckRejectionLeavesLedgerUntouched(t *testing.T) {
	var r Registry[string]
	reject := func(existing []string) error {
		for _, v := range existing {
			if v == "dup" {
				return errors.New("dup 已被占用")
			}
		}
		return nil
	}

	if _, err := r.Register("dup", reject); err != nil {
		t.Fatalf("第一次注册应当成功: %v", err)
	}
	release, err := r.Register("dup", reject)
	if err == nil {
		t.Fatal("重复注册没被 check 拦住")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("报错应点名冲突项，实际: %v", err)
	}
	if release != nil {
		t.Error("被拒绝时不该返回 Release（调用方会拿它去撤销一个不存在的条目）")
	}
	if got := r.All(); len(got) != 1 {
		t.Fatalf("被拒绝后账本 = %v, want 单条", got)
	}
}

// check 拿到的是快照：它改动传进来的切片不能影响账本。
func TestRegistryCheckSeesASnapshot(t *testing.T) {
	var r Registry[string]
	if _, err := r.Register("a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", func(existing []string) error {
		if len(existing) != 1 || existing[0] != "a" {
			return errors.New("已有条目不对")
		}
		existing[0] = "tampered"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.All(); len(got) != 2 || got[0] != "a" {
		t.Fatalf("check 改了内部切片: %v", got)
	}
}

func TestRegistryAllIsACopy(t *testing.T) {
	var r Registry[string]
	if _, err := r.Register("a", nil); err != nil {
		t.Fatal(err)
	}
	r.All()[0] = "tampered"
	if got := r.All(); got[0] != "a" {
		t.Fatalf("All 返回了内部切片: %v", got)
	}
}

func TestRegistryZeroValueIsUsable(t *testing.T) {
	var r Registry[int]
	if got := r.All(); got != nil {
		t.Fatalf("空账本应返回 nil，实际 %v", got)
	}
	if _, err := r.Register(1, nil); err != nil {
		t.Fatalf("零值不可用: %v", err)
	}
	if got := r.All(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("零值注册后 = %v", got)
	}
}
