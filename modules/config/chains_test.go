package config

import (
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/resolve"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一对锁的是 Config 端口上那个**读**（Chains），因为它是「一屏看全部链」这件事
// 唯一的数据来源，而它的正确性不体现在画面上——画面上链少一站、跳过少一类，看起来
// 都像「这台机器就是这么配的」。所以这里逐条断言那几个**只有读端口知道**的事实：
// 顺序是调用方给的、profile 不存在要报错、maxAttempts 不许剪链、一次快照一个时刻。

// chainsFor 是唯一一处允许这么取端口的地方：这里测的正是那个方法本身。
func chainsFor(t *testing.T, profile string, keys ...string) *Chains {
	t.Helper()
	out, err := (&port{}).Chains(profile, keys...)
	if err != nil {
		t.Fatalf("Chains(%q, %v) 失败: %v", profile, keys, err)
	}
	return out
}

// seedProvidersIn **不重复开沙箱**：testkit.Sandbox 每次调用都换一个新的临时目录
// （`t.TempDir()` 每次给一个唯一路径），所以在同一个测试里再开一次会把前面 seed 的
// 那几个文件全落在上一份目录里——症状是「明明写了 demo.kv 却说没有这份档位」。
// providers_test.go 里那个 seedProviders 自带 Sandbox，是因为它的每个测试都从零开始。
func seedProvidersIn(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// 布局：一份 demo 打头（它是默认，所以是链头），一份 alt 挂在后面。
//
// demo 自己把 heavy 绑到 ark，别的档位靠 @heavy 引用；alt 补一个 demo 没有的
// light。这样一次就能同时看到「链头是 demo」「跨 profile 的链」「引用展开」。
func seedChains(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	seedProvidersIn(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"},
		"side":{"base_url":"https://side.example","api_key":"k"}
	}}`)
	seedFile(t, "demo.kv", strings.Join([]string{
		"desc=demo base",
		"heavy=ark/deepseek-v3,ark/deepseek-lite",
		"normal=@heavy",
		"",
	}, "\n"))
	seedFile(t, "alt.kv", strings.Join([]string{
		"desc=alt base",
		"prio=50",
		"heavy=zhipu/glm-4",
		"light=zhipu/glm-4-flash",
		"",
	}, "\n"))
	if err := store.SetDefaultProfile("demo", false); err != nil {
		t.Fatalf("设默认档位失败: %v", err)
	}
}

// 空 profile = 此刻全局生效的那一份，且要**自报**它是不是生效的那一份。
//
// 这一格是界面上「当前配置」那个绿色标记的唯一来源（与 profileConcept 的 Note
// 同一件事）。读端口不报的话，界面只能自己拿 state.json 里的默认名去比——那又是
// 把配置的知识抄进界面。
func TestChainsWithoutAProfileMeansTheDefaultOne(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "", "heavy")
	if out.Profile != "demo" {
		t.Fatalf("没给 profile 时应当落在默认的那一份（demo），实际 %q", out.Profile)
	}
	if !out.Default {
		t.Error("demo 就是此刻生效的那一份，Default 应当是 true")
	}
	if got := out.Keys[0].Steps[0].Binding.String(); got != "ark/deepseek-v3" {
		t.Errorf("链头应当是 ark/deepseek-v3，实际 %q", got)
	}

	// 点名另一份：链头跟着换，而 Default 必须翻成 false（它不是生效的那一份）。
	alt := chainsFor(t, "alt", "heavy")
	if alt.Default {
		t.Error("alt 不是生效的那一份，Default 应当是 false")
	}
	if got := alt.Keys[0].Steps[0].Binding.String(); got != "zhipu/glm-4" {
		t.Errorf("点名 alt 之后链头应当是 zhipu/glm-4，实际 %q", got)
	}
}

// 档位顺序**原样还回去**：谁在前谁在后是产品取舍（`domain.Roles` 是能力从高到低），
// 读端口不重排。
//
// 这条断言防的是「顺手按字母序排一下」——那会让界面上的档位次序变成 h/l/m/n，
// 而 `newgate status` 那边仍是能力序，同一份配置两个屏幕两种排法。
func TestChainsKeepsTheCallersKeyOrder(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "alt", "light", "heavy")
	var got []string
	for _, k := range out.Keys {
		got = append(got, k.Key)
	}
	if strings.Join(got, ",") != "light,heavy" {
		t.Errorf("键的次序应当是调用方给的 light,heavy，实际 %v", got)
	}

	// 没给键 = 跟着 domain.Roles（框架那五档）。
	all := chainsFor(t, "alt")
	if len(all.Keys) != len(domain.Roles) {
		t.Fatalf("没给键时应当列出全部 %d 档，实际 %d 档", len(domain.Roles), len(all.Keys))
	}
	for i, k := range all.Keys {
		if k.Key != domain.Roles[i] {
			t.Errorf("第 %d 档应当是 %q，实际 %q", i, domain.Roles[i], k.Key)
		}
	}
}

// 不存在的 profile **报错**，不给一份空链。
//
// 空链的形状是「这份档位什么都没配」，而事实是「没有这份档位」。静默的话界面上
// 会画出一张空卡，读的人以为自己的配置丢了——而真正的原因（名字打错、文件被删）
// 一个字都看不到。
func TestChainsRejectsAnUnknownProfile(t *testing.T) {
	seedChains(t)

	_, err := (&port{}).Chains("nope", "heavy")
	if err == nil {
		t.Fatal("问一份不存在的档位应当报错，实际给了答案")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("报错里应当带上问的那个名字，实际 %q", err.Error())
	}
}

// 链**不截断**：maxAttempts 是单次请求的执行上限，不是链的 membership。
//
// 这条锁的是一个真实踩过的观感 bug（2026-09-17）：`newgate status` 传了 Attempts()
// 之后第 4 站被静默裁掉，于是「normal 档里怎么没有 ark」查了半天——ark 一直在链上，
// 只是屏幕上看不见。读端口要是也这么传，界面会把同一句话再说一遍。
func TestChainsDoesNotTruncateAtMaxAttempts(t *testing.T) {
	seedChains(t)

	// 把执行上限压到 1 再读：链必须还是完整的那几条。
	st := store.LoadState()
	st.Chain.MaxAttempts = 1
	if err := store.SaveState(st); err != nil {
		t.Fatalf("写 state 失败: %v", err)
	}

	out := chainsFor(t, "demo", "heavy")
	steps := out.Keys[0].Steps
	if len(steps) < 2 {
		t.Fatalf("heavy 该有 2 站（ark/deepseek-v3, ark/deepseek-lite），"+
			"实际 %d 站：maxAttempts 不该剪链", len(steps))
	}
	for _, s := range out.Keys[0].Skips {
		if s.Kind == resolve.SkipMaxSteps {
			t.Errorf("出现了 %q 这类跳过：读端口不该按 maxAttempts 截断", resolve.SkipMaxSteps)
		}
	}
}

// 引用的链**跨 profile**，而且 Step 自己报来源。
//
// demo 的 normal 写成 `@heavy`，展开出来两站都属于 demo——这是引用；而
// `newgate tier` 那边的链头之后会接上别的 profile（按 priority 排）。两种都要
// 写出来源：不写的话用户会去自己正看着的那份文件里找一个不存在的候选。
func TestChainsReportsWhereEachStepCameFrom(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "normal")
	steps := out.Keys[0].Steps
	if len(steps) == 0 {
		t.Fatal("normal 档解析出来是空的：@heavy 的引用没有展开")
	}
	for i, s := range steps {
		if s.Profile == "" {
			t.Errorf("第 %d 站没写来自哪份 profile（%s）", i, s.Binding.String())
		}
	}
	// 引用就地展开：demo 的 normal 与 heavy 是同一条链。
	heavy := chainsFor(t, "demo", "heavy")
	if len(steps) != len(heavy.Keys[0].Steps) {
		t.Errorf("normal=@heavy 展开出 %d 站，而 heavy 自己是 %d 站——引用没有原地展开",
			len(steps), len(heavy.Keys[0].Steps))
	}
}

// 被排除的那一份在 Skips 里出现，而不是从屏幕上消失。
//
// 它与 Steps 是两件事：被跳过的候选常常一步都不在链上（这一条就是），所以只报
// Steps 的话「我明明配了这一份」与「它为什么没进链」都无处可说。
func TestChainsReportsSkippedProfiles(t *testing.T) {
	seedChains(t)
	seedFile(t, "side.kv", "prio=90\nexcluded=true\nheavy=side/model\n")

	out := chainsFor(t, "demo", "heavy")
	found := false
	for _, s := range out.Keys[0].Skips {
		if s.Profile == "side" && s.Kind == resolve.SkipExcluded {
			found = true
		}
	}
	if !found {
		t.Errorf("被排除的 side 应当出现在 Skips 里（kind=%q），实际 %+v",
			resolve.SkipExcluded, out.Keys[0].Skips)
	}
	for _, s := range out.Keys[0].Steps {
		if s.Binding.Provider == "side" {
			t.Error("被排除的 side 不该出现在链上")
		}
	}
}

// 写坏的那一份不许把整次查询弄挂——它被 store.Load 跳过，其余照常。
//
// 这一条是「一个手改坏的文件不该让所有 agent 停摆」那条规矩在读端口上的样子：
// 读端口要是把 Load 的错误直接透出去，界面会白屏，而磁盘上别的配置是好的。
func TestChainsSurvivesABrokenProfileFile(t *testing.T) {
	seedChains(t)
	// 冒号不是 `=`：kv 解析器会拒（见 ParseProfileKV 的 unknown key 分支）。
	seedFile(t, "broken.kv", "normal: ark/deepseek-v3\n")

	out := chainsFor(t, "demo", "heavy")
	if len(out.Keys[0].Steps) == 0 {
		t.Error("一个坏文件把整次查询弄空了：好的那些档位应当照常报出来")
	}
}

// 端口的**读**与**写**是同一条路：面板上那个 `Chains` 拿到的，必须就是
// 命令行 `newgate tier` 拿到的（同一次快照、同一个 resolve）。
//
// 这条断言防的是「面板自己拼一份」——那种实现今天看起来对，等 resolve 改了
// （多一种 Skip、排序规则变了）就会悄悄和 CLI 分叉。
func TestChainsAgreesWithTheCommandLine(t *testing.T) {
	seedChains(t)

	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := resolve.BuildChain("heavy", snap.Profiles, snap.Providers,
		resolve.Opts{Active: snap.State.DefaultProfile, MaxSteps: 0})

	got := chainsFor(t, "", "heavy").Keys[0].Steps
	if len(got) != len(want) {
		t.Fatalf("端口给了 %d 站，resolve 直接算出来是 %d 站", len(got), len(want))
	}
	for i := range got {
		if got[i].String() != want[i].String() {
			t.Errorf("第 %d 站：端口 %q，resolve %q", i, got[i].String(), want[i].String())
		}
	}
}

// 主目录那份 state 里 Chain 的字段名只有一种拼法，读端口不该顺手给它起别名。
// （`store.LoadState` 会在缺省时补上值，所以这里只确认写进去的能读回来。）
func TestChainsReadsTheSameSnapshotAsEverythingElse(t *testing.T) {
	seedChains(t)

	if _, err := os.Stat(paths.StateFile()); err != nil {
		t.Fatalf("state.json 没落盘：%v", err)
	}
	if got := chainsFor(t, "", "heavy").Profile; got != store.LoadState().DefaultProfile {
		t.Errorf("端口说链头是 %q，而 state 里默认是 %q——两者读的不是同一份快照",
			got, store.LoadState().DefaultProfile)
	}
}
