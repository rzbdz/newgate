package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 一份档位文件的**全部动作**都挂在它自己那张 kv 卡上（applyProfileRoles）：
// 改档位、改继承、改名、删除、新建。
//
// 2026-09-20 之前这些散在两张卡上（一张「档位文件」目录卡管新增/改名/删除，每个
// profile 一张卡管档位），用户的原话是那张目录卡「多余的啊，因为所有单位都有自己的
// kv」——于是它没了，动作都收到了卡自己身上。下面这些锁的就是收拢之后的正确性。
//
// 最要紧的一条仍然是**删除**：它删的是磁盘上的配置，而「把别人刚写的东西删了」
// 是这类动作最不可原谅的一种错（CAS 就是为此存在的，见 store.RemoveIfUnchanged）。

// seedFile 把一份档位文件落盘，返回绝对路径。**每个测试自己先 testkit.Sandbox(t)**
// ——沙箱一次就够；这一层不重复 Sandbox，是因为重复会把 NEWGATE_HOME 切到另一份
// 新临时目录，前面写的那份就没了。
func seedFile(t *testing.T, rel, body string) string {
	t.Helper()
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	abs := paths.Mappings() + "/" + rel
	if err := os.WriteFile(abs, []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
	return abs
}

// seedDemo 在沙箱里落一份演示档位文件（调用者先开沙箱），返回它的绝对路径。
func seedDemo(t *testing.T) string {
	t.Helper()
	testkit.Sandbox(t)
	return seedFile(t, "demo.kv", "desc=demo base\nnormal=p/demo-model\n")
}

// edit 对某一份文件跑一次那张卡的 apply。base 为空表示「不按基线判」（新建 / 只改
// 元数据时用不到）。
func edit(t *testing.T, abs, body, base string) error {
	t.Helper()
	_, err := applyProfileRoles("config.profile."+strings.TrimSuffix(filepathBase(abs), ".kv"), abs, []byte(body), base)
	return err
}

func filepathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// TestCreateAddsAFile：`{"create":"名字"}` 建一份新档位文件。
func TestCreateAddsAFile(t *testing.T) {
	seedDemo(t)
	if err := edit(t, paths.Mappings()+"/demo.kv", `{"create":"fresh"}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Mappings() + "/fresh.kv"); err != nil {
		t.Fatalf("新建的档位文件没落盘: %v", err)
	}
	// 重名要拦住：那是「把已有的那份覆盖掉」，最危险的一种静默。
	if err := edit(t, paths.Mappings()+"/demo.kv", `{"create":"demo"}`, ""); err == nil {
		t.Error("建一份已经存在的档位该被拒绝")
	}
	// 越界的名字要拦住（能写到 mappings/ 外面去）。
	for _, bad := range []string{"../escape", "a/b", ".."} {
		body := `{"create":"` + bad + `"}`
		if err := edit(t, paths.Mappings()+"/demo.kv", body, ""); err == nil {
			t.Errorf("越界的名字该被拒绝: %s", bad)
		}
	}
}

// TestDeleteRemovesTheFile：删除真的删盘，且**按加载时的基线判**——别人抢先改了
// 就报错，而不是把新内容一并删掉。这是这张卡唯一真正危险的动作。
func TestDeleteRemovesTheFile(t *testing.T) {
	abs := seedDemo(t)
	if err := edit(t, abs, `{"delete":true}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("删除后文件还在: %v", err)
	}
}

func TestDeleteWithStaleBaseIsRejected(t *testing.T) {
	abs := seedDemo(t)
	if err := os.WriteFile(abs, []byte("desc=新内容，别人刚改\n"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := edit(t, abs, `{"delete":true}`, "sha256:旧"); err == nil {
		t.Fatal("基线过期还让删了？那会把别人刚写的东西删掉")
	}
	if _, serr := os.Stat(abs); serr != nil {
		t.Fatal("被拒的删除不该动盘")
	}
}

// TestRenamingMovesTheRolesOver：改名 = 建新文件、删旧文件，**档位原样搬过去**。
func TestRenamingMovesTheRolesOver(t *testing.T) {
	abs := seedDemo(t)
	if err := edit(t, abs, `{"name":"renamed"}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Error("旧文件没删")
	}
	b, err := os.ReadFile(paths.Mappings() + "/renamed.kv")
	if err != nil {
		t.Fatalf("新文件没建: %v", err)
	}
	if !strings.Contains(string(b), "normal=p/demo-model") {
		t.Errorf("改名该把档位一起搬过去，实际:\n%s", b)
	}
}

// TestExtendsIsWrittenAndCleared：继承从界面上直接写/清。
//
// 这是用户点名要的那一格（「这些东西有一个继承的，叫你 ui 上把继承可选做出来」）：
// 选了父档位就写 `extends=`，选回空就把那一行去掉。
func TestExtendsIsWrittenAndCleared(t *testing.T) {
	abs := seedDemo(t)
	seedFile(t, "parent.kv", "normal=p/parent-model\n")

	if err := edit(t, abs, `{"extends":"parent"}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(abs)
	if !strings.Contains(string(b), "extends=parent") {
		t.Errorf("继承没写进去:\n%s", b)
	}
	// 写继承**不该动档位**（roles 没交 = 没改）。
	if !strings.Contains(string(b), "normal=p/demo-model") {
		t.Errorf("只改继承却把档位抹了:\n%s", b)
	}

	if err := edit(t, abs, `{"extends":""}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(abs)
	if strings.Contains(string(b), "extends=") {
		t.Errorf("清空继承之后那一行还在:\n%s", b)
	}
}

// TestRolesStillReplaceWholesale：没交 extends/name 时，行为与以前一模一样——
// 交上来的 roles 就是这一份文件全部的档位。这条防的是「加了新动作，把老动作
// 悄悄改成了增量」。
func TestRolesStillReplaceWholesale(t *testing.T) {
	abs := seedDemo(t)
	body := `{"roles":{"heavy":[{"provider":"p","model":"big"}],"light":[{"provider":"p","model":"small"}]}}`
	if err := edit(t, abs, body, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(abs)
	s := string(b)
	if strings.Contains(s, "normal=") {
		t.Errorf("旧的 normal 该被替换掉（roles 是整体替换）:\n%s", s)
	}
	for _, want := range []string{"heavy=p/big", "light=p/small"} {
		if !strings.Contains(s, want) {
			t.Errorf("少了 %s:\n%s", want, s)
		}
	}
}

// TestStaleWriteIsRejected：改档位也要按基线判——这是界面与命令行同时改一份文件
// 时唯一的防线。
func TestStaleWriteIsRejected(t *testing.T) {
	abs := seedDemo(t)
	if err := edit(t, abs, `{"roles":{"normal":[{"provider":"p","model":"m"}]}}`, "sha256:旧"); err == nil {
		t.Error("基线过期还让写了？两次改动会互相静默覆盖")
	}
}

// TestProfileFamilyTerminatesAndGroups 同时锁**返回值**与**终止性**。
//
// 终止性那条不是多余的：这一段原来写成
// `for i := strings.Index(name, "-"); i > 0; i = strings.Index(name[i+1:], "-") + i + 1`，
// 而在 `name[i+1:]` 里没有横杠时 `Index` 回 -1、`-1+i+1 == i`，下标原地踏步。
// 触发它的正是**界面新建档位的默认名 `new-profile`**（`new` 之后没有第二个横杠，
// 而 `new` 又不是已有档位）——后果是 `/ui/api/snapshot` 死循环、CPU 打满、整个
// 界面卡住（2026-09-20 实测）。
//
// 所以这里用 goroutine + 超时跑：回归的表现是**这段代码不返回**，而不是一条红的
// 断言——不加超时的话，测试套件会跟着一起挂住，谁都看不出挂在哪。
func TestProfileFamilyTerminatesAndGroups(t *testing.T) {
	cases := []struct {
		name string
		all  []string
		want string
	}{
		// 有兄弟：归到那一族。
		{"claude-cheap", []string{"claude", "claude-cheap"}, "claude"},
		{"minimax-fast", []string{"minimax", "minimax-fast"}, "minimax"},
		// 兄弟不存在：自己一族。横杠在档位名里很常见（`gpt-4.1-mini`），
		// 凭「名字里有横杠」分组会造出一堆并不存在的家族。
		{"gpt-4.1-mini", []string{"gpt-4.1-mini"}, "gpt-4.1-mini"},
		// **回归的那一条**：界面「＋新建档位」造出来的名字。
		{"new-profile", []string{"demo", "new-profile"}, "new-profile"},
		{"new-profile-2", []string{"demo", "new-profile", "new-profile-2"}, "new-profile"},
		// 边界：没有横杠、横杠在头尾、只有一段。
		{"demo", []string{"demo"}, "demo"},
		{"-leading", []string{"-leading"}, "-leading"},
		{"trailing-", []string{"trailing-"}, "trailing-"},
		{"-", []string{"-"}, "-"},
	}
	for _, c := range cases {
		done := make(chan string, 1)
		go func() { done <- profileFamily(c.name, c.all) }()
		select {
		case got := <-done:
			if got != c.want {
				t.Errorf("profileFamily(%q, %v) = %q，想要 %q", c.name, c.all, got, c.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("profileFamily(%q, %v) 不返回——它挂住的是所有人共用的读快照那条路", c.name, c.all)
		}
	}
}
