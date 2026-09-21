package i18n

import (
	"encoding/binary"
	"io/fs"
	"os"
	"slices"
	"testing"
)

// catalogsJSONForTest 让测试能读**磁盘上**那份 JSON：它已经不在 embed 里了（嵌的
// 是 bundle），而这几条要验的正是「bundle 与 JSON 一致」。
//
// 住在测试文件里而不是 catalogs.go：运行期不该有走 JSON 的路，那正是这一趟要消掉的
// 开销——发布代码里放一个「只有测试用」的入口，下一个人就会以为它是个正经 API。
func catalogsJSONForTest() fs.FS { return os.DirFS(".") }

// 目录表的编译期形态（见 bundle.go）。这三条锁的是同一条链的三段：编出来的字节
// 读得回来、**读回来的必须与 JSON 那份逐字段相同**、以及嵌在二进制里那一份确实
// 是新的（不是「改了 JSON 忘了重生成」）。
//
// 为什么值得：这个是**派生物**，而派生物的失效方式是静默的——`.bin` 落后于 `.json`
// 时，界面照样跑，只是少了最近加的那几句译文，谁也不会报错。所以「读得回来」不算
// 数，得与 JSON 那份对上才算。

func sampleCatalog() Catalog {
	return Catalog{
		Language: "zh-Hans", Source: "en",
		Widths: map[string]int{"label": 8, "term": 12},
		Messages: map[string]Entry{
			"plain":      {Text: "普通"},
			"plural {n}": {One: "一个", Other: "{n} 个", Reviewed: true},
			"noted":      {Text: "带备注", Note: "给译者看", Machine: true},
			"空":          {},
		},
	}
}

// TestABundleRoundTrips：编出来读回去，必须逐字段相同。
func TestABundleRoundTrips(t *testing.T) {
	want := sampleCatalog()
	got, err := DecodeCatalog(EncodeCatalog(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != want.Language || got.Source != want.Source {
		t.Errorf("语言/来源对不上: %+v", got)
	}
	if len(got.Widths) != len(want.Widths) || got.Widths["label"] != 8 {
		t.Errorf("版式尺寸对不上: %+v", got.Widths)
	}
	if len(got.Messages) != len(want.Messages) {
		t.Fatalf("条数对不上: %d vs %d", len(got.Messages), len(want.Messages))
	}
	for id, w := range want.Messages {
		g, ok := got.Messages[id]
		if !ok {
			t.Errorf("少了一条 %q", id)
			continue
		}
		if g != w && !(g.Empty() && w.Empty()) {
			t.Errorf("%q 对不上:\n  想要 %+v\n  实际 %+v", id, w, g)
		}
	}
	// 复数与标记是**容易在二进制里丢**的两样（它们不是文本，是形状）。
	if e := got.Messages["plural {n}"]; !e.plural() || !e.Reviewed {
		t.Errorf("复数/复核标记丢了: %+v", e)
	}
	if e := got.Messages["noted"]; !e.Machine || e.Note != "给译者看" {
		t.Errorf("机翻标记/备注丢了: %+v", e)
	}
}

// TestABundleIsDeterministic：同一份输入编两次必须逐字节相同。
//
// 这条是 `-check` 能成立的前提：不确定的话，它每次都会报「过期」，而那种假警报
// 会让人把这条检查关掉——那时它拦不住的那个真错误就再也拦不住了。
func TestABundleIsDeterministic(t *testing.T) {
	a := EncodeCatalog(sampleCatalog())
	b := EncodeCatalog(sampleCatalog())
	if string(a) != string(b) {
		t.Error("同一份目录编出来的字节不一样（map 遍历序漏进输出里了？）")
	}
}

// TestABundleRefusesGarbage：坏输入必须报错，**不能崩**。
//
// 目录表是在 Start 里读的，一次 panic 会让整个进程起不来（连 `newgate status` 都
// 跑不了，而那条命令正是用来查「为什么起不来」的）。所以越界一律转成错误。
func TestABundleRefusesGarbage(t *testing.T) {
	good := EncodeCatalog(sampleCatalog())
	cases := map[string][]byte{
		"不是 bundle":  []byte("hello"),
		"魔数对、身子截断":   good[:20],
		"砍掉最后 10 字节": good[:len(good)-10],
		"长度字段是垃圾":    append(append([]byte(nil), bundleMagic...), 'c', 0xff, 0xff, 0xff, 0xff),
	}
	for name, raw := range cases {
		if _, err := DecodeCatalog(raw); err == nil {
			t.Errorf("%s：该报错，却读成功了", name)
		}
	}
	// 类型对不上也要报（拿账本去当译文读）。
	if _, err := DecodeCatalog(EncodeLedger(Ledger{Messages: map[string]LedgerEntry{"x": {Args: []string{"y"}}}})); err == nil {
		t.Error("账本被当成译文读成功了，该报错")
	}
}

// TestAHostileCountDoesNotAllocate：条数字段是**文件里写着的数**，不能直接拿去
// `make(map, n)`。
//
// 这一条防的不是「读出错数据」而是「读的时候先崩」：`make(map[K]V, 40 亿)` 会在
// 第一处边界检查生效**之前**先要一次巨额分配——症状是启动时 OOM，而不是一句
// 「这个 bundle 是坏的」。而目录表是在 Start 里读的，起不来就等于 `newgate status`
// 也跑不了，而那条命令正是用来查「为什么起不来」的。
//
// 断言写成「不 panic 且报错」：分配那个大小在测试机上会直接崩，所以它红了就是这条
// 检查真的有用。
func TestAHostileCountDoesNotAllocate(t *testing.T) {
	mk := func(kind byte, count uint32) []byte {
		raw := append([]byte(nil), bundleMagic...)
		raw = append(raw, kind)
		if kind == bundleKindCatalog {
			raw = append(raw, 0, 0, 0, 0) // language 空串
			raw = append(raw, 0, 0, 0, 0) // source 空串
			raw = append(raw, 0, 0, 0, 0) // widths: 0 个
		}
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], count)
		return append(raw, tmp[:]...)
	}
	huge := uint32(0xFFFFFFF0)
	if _, err := DecodeCatalog(mk(bundleKindCatalog, huge)); err == nil {
		t.Error("条数是天文数字时该报错")
	}
	// 账本同理：它的条数字段在更前面，走的是另一条 make。
	if _, err := DecodeLedger(mk(bundleKindLedger, huge)); err == nil {
		t.Error("账本条数是天文数字时该报错")
	}
}

// TestTheEmbeddedBundlesMatchTheJSON：嵌在二进制里的那一份必须与仓库里的 JSON 相同。
//
// 这就是「改了 JSON 忘了跑 bundle」的棘轮。判据落在**内容**上：把内嵌的 bundle 解
// 开、把磁盘上的 JSON 解出来，两份比字段。
func TestTheEmbeddedBundlesMatchTheJSON(t *testing.T) {
	led, cats, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(led.Messages) == 0 || len(cats) == 0 {
		t.Fatal("内嵌的目录表是空的")
	}
	fromJSON, err := ledgerFromFS(catalogsJSONForTest(), "catalogs")
	if err != nil {
		t.Fatal(err)
	}
	if len(fromJSON.Messages) != len(led.Messages) {
		t.Fatalf("账本条数：bundle %d，JSON %d —— 改了 JSON 没跑 `tools/i18n bundle`？",
			len(led.Messages), len(fromJSON.Messages))
	}
	for id, want := range fromJSON.Messages {
		got, ok := led.Messages[id]
		if !ok {
			t.Errorf("账本里少了 %q（bundle 落后于 JSON？）", id)
			continue
		}
		if got.One != want.One ||
			got.Other != want.Other || got.Note != want.Note ||
			!slices.Equal(got.Args, want.Args) {
			t.Errorf("账本 %q 对不上（bundle 落后于 JSON？）\n  想要 %+v\n  实际 %+v", id, want, got)
		}
	}
	jsonCats, err := CatalogsFromFS(catalogsJSONForTest(), "catalogs")
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonCats) != len(cats) {
		t.Fatalf("语言份数：bundle %d，JSON %d", len(cats), len(jsonCats))
	}
	for i := range jsonCats {
		want, got := jsonCats[i], cats[i]
		if want.Language != got.Language || len(want.Messages) != len(got.Messages) {
			t.Fatalf("%s 对不上（bundle 落后于 JSON？）", want.Language)
		}
		for id, we := range want.Messages {
			ge, ok := got.Messages[id]
			if !ok || ge != we {
				t.Errorf("%s / %q 对不上（bundle 落后于 JSON？）\n  想要 %+v\n  实际 %+v", want.Language, id, we, ge)
			}
		}
	}
}
