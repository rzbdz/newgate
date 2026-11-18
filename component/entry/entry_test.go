package entry

import "testing"

// TestReleasingOneHandlerDoesNotRemoveAnother 锁住「撤销按 token、不按位置」。
//
// 现场：seq 原来是切片长度。注册 A、注册 B、撤销 A、再注册 C —— C 拿到与 B
// 相同的 seq，C 的 Release 于是删掉了 B。症状是「某个跑着的入口突然没人认领」，
// 而它只在「先撤销再注册」的顺序里出现（模块重启、测试夹具、插件换绑）。
func TestReleasingOneHandlerDoesNotRemoveAnother(t *testing.T) {
	table := NewTable()
	releaseA, err := table.Register(fakeHandler{name: "a"}, DefaultRank)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Register(fakeHandler{name: "b"}, DefaultRank); err != nil {
		t.Fatal(err)
	}
	if err := releaseA(); err != nil {
		t.Fatal(err)
	}
	releaseC, err := table.Register(fakeHandler{name: "c"}, DefaultRank)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseC(); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, h := range table.Handlers() {
		names[h.Name()] = true
	}
	if !names["b"] {
		t.Fatalf("撤销 c 时把 b 也删掉了——seq 又变成位置了: %v", table.Handlers())
	}
	if names["a"] || names["c"] {
		t.Fatalf("该留下的只有 b: %v", names)
	}
}

type fakeHandler struct{ name string }

func (f fakeHandler) Name() string        { return f.name }
func (f fakeHandler) Claims(Process) bool { return false }
func (f fakeHandler) Handle(Process) int  { return 0 }
