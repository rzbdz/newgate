package entry

import "testing"

// TestReleasingOneHandlerDoesNotRemoveAnother locks down "undo by token, not by position".
//
// The scene: seq used to be the slice length. Register A, register B, release A, then register
// C -- C gets the same seq as B, so C's Release deletes B. The symptom is "some running entry
// suddenly has no claimant", and it only appears in the "release then register" order (module
// restart, test fixtures, plugin rebinding).
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
