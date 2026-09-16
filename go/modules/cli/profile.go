package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/domain"
	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/config/resolve"
	"github.com/rzbdz/newgate/go/modules/config/store"
	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/gateway/health"
	"github.com/rzbdz/newgate/go/modules/runtime/daemon"
)

// cmdSetProfile switches the chain head. An empty agent sets the global
// default; naming an agent switches only that agent, leaving every other
// agent — including ones with sessions in flight — untouched.
func cmdSetProfile(agent, name string) int {
	if err := store.SetActiveProfile(agent, name); err != nil {
		return die(65, err.Error())
	}
	scope := "全局默认"
	if agent != "" {
		scope = "agent " + agent
	}
	fmt.Println(style.Item(style.OK, scope+" → "+style.Cyan(name)))

	if pr, err := store.LoadProfile(name); err == nil {
		if pr.Description != "" {
			fmt.Println(style.Hint(pr.Description))
		}
		if pr.Pinned {
			fmt.Println(style.Hint("pinned：链到此为止，失败直接上报，不再替换候选"))
		}
		t := style.NewTable("档位", "绑定")
		for _, tier := range domain.Roles {
			if b, ok := pr.Resolve(tier); ok {
				t.Row(style.Cyan(tier), b.String())
			}
		}
		if t.Len() > 0 {
			fmt.Println()
			fmt.Print(t.String())
		}
	}
	notifyProxy()
	fmt.Println()
	if daemon.Running() != nil {
		fmt.Println(style.Hint("即刻生效；已在运行的会话不受影响"))
	} else {
		fmt.Println(style.Hint("代理未运行 · newgate start"))
	}
	return 0
}

// cmdProfiles 列出全部 profile。
//
// 一屏回答三个问题：默认是哪个、优先级怎么排、哪个 agent 挂了别名的 profile。
// 标了标志的 profile 才是异常的（pinned / excluded），所以不加额外段落，
// 全部信息压在一张表里——段落一多，扫读就变成阅读。
func cmdProfiles() int {
	names, err := store.ListProfiles()
	if err != nil {
		return die(65, "cannot read mappings: "+err.Error())
	}
	st := store.LoadState()
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := store.LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Prio() != ps[j].Prio() {
			return ps[i].Prio() < ps[j].Prio()
		}
		return ps[i].Name < ps[j].Name
	})

	fmt.Println(style.Title("newgate profiles",
		fmt.Sprintf("%d 个 · 默认 %s", len(ps), st.DefaultProfile)))
	fmt.Println(style.Rule(72))

	t := style.NewTable("优先级", "profile", "标志", "说明")
	t.AlignRight(0)
	for _, p := range ps {
		var flags []string
		if p.Name == st.DefaultProfile {
			flags = append(flags, style.Green("default"))
		}
		if p.Pinned {
			flags = append(flags, style.Yellow("pinned"))
		}
		if p.Excluded {
			flags = append(flags, style.Yellow("excluded"))
		}
		for agent, ap := range st.Active {
			if ap == p.Name {
				flags = append(flags, style.Cyan("←"+agent))
			}
		}
		name := p.Name
		if p.Name == st.DefaultProfile {
			name = style.Cyan(p.Name)
		}
		t.Row(fmt.Sprintf("%d", p.Prio()), name, strings.Join(flags, " "),
			style.Dim(p.Description))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint("pinned 停在链首不替换 · excluded 只能被显式选中 · ←agent 该 agent 单独用这个 profile"))
	return 0
}

// cmdProfileKV 把一个 profile 转成 KV 文本——json→kv 的转换口。
//
// 不带 --write 只打印（拷走、改完再贴回来都行）；带 --write 落盘成
// mappings/<名>.kv 并把旧 .json 改名 .bak（kv 优先于 json，留着旧文件
// 会永远被压着，退役比并存干净）。
func cmdProfileKV(args []string) int {
	if len(args) < 1 || args[0] == "" {
		return die(64, "用法：newgate profile kv <名> [--write]")
	}
	name := args[0]
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return die(65, err.Error())
	}
	text := store.SerializeProfileKV(raw)

	if len(args) < 2 || args[1] != "--write" {
		fmt.Print(text)
		fmt.Println(style.Dim("# 落盘：newgate profile kv " + name + " --write"))
		return 0
	}
	kvPath := filepath.Join(paths.Mappings(), name+".kv")
	if err := os.WriteFile(kvPath, []byte(text), 0o660); err != nil {
		return die(70, "写 "+kvPath+" 失败: "+err.Error())
	}
	jsonPath := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		if err := os.Rename(jsonPath, jsonPath+".bak"); err != nil {
			return die(70, "旧 json 改名失败（kv 已写入，手动处理）: "+err.Error())
		}
		fmt.Println(style.Item(style.OK, kvPath+style.Dim("   旧 .json → .json.bak")))
	} else {
		fmt.Println(style.Item(style.OK, kvPath))
	}
	notifyProxy()
	return 0
}