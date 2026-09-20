package view

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// 账本的行为与渲染无关，所以它在叶子里就能测——界面的测试要拖起 HTTP 与嵌入
// 资源，而这里要锁的是几件很具体的事：**注册不产出**（每一次 CLI 调用都会跑到
// 注册，而没有人会打开界面）、重名必须报错、撤销只撤自己那条、并发安全。

// TestRegisterDoesNotProduce 是这套东西最要紧的一条：登记一个产出函数**不能**
// 顺带把数据算出来。
//
// 它守的不是性能，是「每条 `newgate …` 命令都白读一遍全部配置」那个坑——第一版
// 就是那样（Start 里直接读出全部数据塞进概念），被删掉了。这个测试红了就说明
// 那个坑回来了。
func TestRegisterDoesNotProduce(t *testing.T) {
	r := NewRegistry()
	var calls atomic.Int64
	if _, err := r.Register("config", func() ([]Concept, error) {
		calls.Add(1)
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("注册时产出了 %d 次——每一次 CLI 调用都会跑到注册", n)
	}
	if _, err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("快照时该产出一次，实际 %d 次", n)
	}
}

// TestSnapshotAsksEveryTime：界面上的刷新得是真的刷新——CLI 刚建的档位文件要
// 出现在下一次快照里。缓存一份旧的等于让 daemon 的界面停在启动那一刻。
func TestSnapshotAsksEveryTime(t *testing.T) {
	r := NewRegistry()
	n := 0
	if _, err := r.Register("config", func() ([]Concept, error) {
		n++
		return []Concept{{ID: "profile." + string(rune('0'+n)), Kind: KindMapping}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := r.Snapshot()
	second, _ := r.Snapshot()
	if first[0].ID == second[0].ID {
		t.Fatalf("两次快照拿到了同一个概念（%s）——产出函数只跑了一次", first[0].ID)
	}
}

func TestSnapshotRejectsDuplicateID(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "one", Concept{ID: "a", Kind: KindCode})
	mustRegister(t, r, "two", Concept{ID: "a", Kind: KindCode})
	_, err := r.Snapshot()
	if err == nil {
		t.Fatal("同一个 ID 被两家报上来该报错——否则界面上只会显示其中一个，" +
			"另一个的修改点永远点不到")
	}
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错要点名两家（谁和谁撞了），缺 %q: %v", want, err)
		}
	}
}

func TestSnapshotRequiresIdentityAndKind(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{Kind: KindCode})
	if _, err := r.Snapshot(); err == nil {
		t.Error("没有 ID 的概念该被拒绝（身份是前端定位它的唯一依据）")
	}
	r2 := NewRegistry()
	mustRegister(t, r2, "x", Concept{ID: "a"})
	if _, err := r2.Snapshot(); err == nil {
		t.Error("没有 Kind 的概念该被拒绝（界面不知道怎么渲染它）")
	}
}

// TestLedgerStampsTheSource：来源由账本填，不用贡献者自己报。报错了就是替别人
// 背锅（报错的文案里点名的是别人），而账本本来就知道这条是谁登记的。
func TestLedgerStampsTheSource(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "config", Concept{ID: "a", Kind: KindCode, Source: "撒谎的模块"})
	got, _ := r.Snapshot()
	if got[0].Source != "config" {
		t.Errorf("Source 该由账本填，实际 %q", got[0].Source)
	}
}

func TestRegisterRejectsEmptySourceAndNilContributor(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("", func() ([]Concept, error) { return nil, nil }); err == nil {
		t.Error("没有来源名的贡献者该被拒绝（报错与分组都要点名）")
	}
	if _, err := r.Register("x", nil); err == nil {
		t.Error("nil 产出函数该被拒绝（界面上会静默什么都没有）")
	}
}

// TestReleaseOnlyRemovesItsOwn：撤销按**登记的序号**认人，不按名字。同一个模块
// 名重复登记是允许的，后来者不该被前一个的撤销带走。
func TestReleaseOnlyRemovesItsOwn(t *testing.T) {
	r := NewRegistry()
	first, err := r.Register("config", func() ([]Concept, error) {
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first(); err != nil {
		t.Fatal(err)
	}
	if all, _ := r.Snapshot(); len(all) != 0 {
		t.Fatalf("撤销之后不该还在: %+v", all)
	}
	if err := first(); err != nil {
		t.Fatalf("撤销该是幂等的: %v", err)
	}
	if _, err := r.Register("config", func() ([]Concept, error) {
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = first() // 再撤一次（幂等，且不许误删后来者那条）
	if all, _ := r.Snapshot(); len(all) != 1 {
		t.Fatalf("后来者的概念被误删了: %+v", all)
	}
}

// TestSnapshotIsSortedAndStable：诊断与前端分组都靠它。同一份装配跑两次，顺序
// 必须一样——不然用户会以为东西变了。
func TestSnapshotIsSortedAndStable(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "gateway", Concept{ID: "z", Kind: KindSeries})
	mustRegister(t, r, "config", Concept{ID: "b", Kind: KindCode}, Concept{ID: "a", Kind: KindCode})
	got, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"config/a", "config/b", "gateway/z"}
	for i, c := range got {
		if c.Source+"/"+c.ID != want[i] {
			t.Fatalf("Snapshot()[%d] = %s/%s，想要 %s", i, c.Source, c.ID, want[i])
		}
	}
}

// TestApplyCarriesTheEditThrough：概念不只报数据，还带**怎么改**。这条锁的是
// 「写的知识属于拥有那份数据的人」——BFF 只负责把前端那坨 JSON 原样转交。
func TestApplyCarriesTheEditThrough(t *testing.T) {
	r := NewRegistry()
	var got json.RawMessage
	var gotBase string
	mustRegister(t, r, "x", Concept{
		ID: "a", Kind: KindCode,
		Apply: func(edit json.RawMessage, base string) (string, error) {
			got, gotBase = edit, base
			return "sha256:new", nil
		},
	})
	c, err := r.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if c.Apply == nil {
		t.Fatal("Apply 没进快照——界面就永远写不了")
	}
	rev, err := c.Apply(json.RawMessage(`{"text":"hi"}`), "sha256:old")
	if err != nil || rev != "sha256:new" {
		t.Fatalf("Apply 的返回值该原样回给界面: rev=%q err=%v", rev, err)
	}
	if string(got) != `{"text":"hi"}` || gotBase != "sha256:old" {
		t.Errorf("编辑内容与基线该原样转交: edit=%s base=%s", got, gotBase)
	}
}

// TestGetReportsUnknownConcept：界面拿这个区分 404（前端那份快照过期了，重载
// 就好）与 500（某种真的坏了）。
func TestGetReportsUnknownConcept(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nope")
	if !errors.Is(err, ErrUnknownConcept) {
		t.Fatalf("要能 errors.Is(ErrUnknownConcept) 认出来，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("报错里要有那个 ID（排查时第一个要问的就是它）: %v", err)
	}
}

// TestConcurrentRegisterAndSnapshot：模块的 Start（注册）与界面的渲染（快照）
// 跑在不同 goroutine 上。跑 -race 才有意义。
func TestConcurrentRegisterAndSnapshot(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		id := string(rune('a' + i))
		go func() {
			defer wg.Done()
			rel, err := r.Register("s"+id, func() ([]Concept, error) {
				return []Concept{{ID: id, Kind: KindCode}}, nil
			})
			if err != nil {
				t.Errorf("Register(%s): %v", id, err)
				return
			}
			rel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = r.Snapshot()
				_, _ = r.Get(id)
			}
		}()
	}
	wg.Wait()
}

func mustRegister(t *testing.T, r *Registry, source string, concepts ...Concept) {
	t.Helper()
	if _, err := r.Register(source, func() ([]Concept, error) { return concepts, nil }); err != nil {
		t.Fatal(err)
	}
}
