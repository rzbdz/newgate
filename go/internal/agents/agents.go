// Package agents 是被接管的 CLI 的描述符表。
//
// 术语（与 docs/00-index.md 词表一致）：
//
//	agent        被接管的 CLI 本身 —— claude / opencode / codex
//	intra-agent  agent 内部的一个可配置子 agent —— 西西弗斯 / oracle / quick
//	tier         能力档 —— heavy / mid / light / vision
//	model        真实模型 —— 具体的上游模型 id
//
// 描述符是**数据**，不是代码。接一个新 agent 的成本应该是「加一个结构体
// 字面量」，而不是「改一堆 if-else」。这是这个项目能不能长期活下去的关键
// （docs/02-architecture.md §5）。
package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Slot agent 内部一个可配置的模型位置。
//
// 两种形状：
//   - claude 的槽位是**档位形**：opus / sonnet / haiku / fable / subagent，
//     由 ANTHROPIC_DEFAULT_*_MODEL 这几个环境变量控制
//   - opencode+omo 的槽位是**命名 intra-agent 形**：西西弗斯 / oracle /
//     quick 等 19 个，写在配置文件里
//
// 统一成 Slot 之后，两者的差别只是「怎么落地」（env 还是配置文件），
// 而不是两套并行的机制。
type Slot struct {
	// Name 槽位名。claude 是 opus/sonnet/...；opencode 是 intra-agent 名。
	Name string
	// Tier 这个槽位默认映射到哪个能力档。
	//
	// 这一层间接是为了让「只有两个模型的 provider 家族」也只写两行：
	// 19 个 intra-agent 映射到 3 个 tier，profile 只需给 tier 绑定。
	// 想让某个 intra-agent 偏离默认，才单独覆盖它（docs/18 §2 稀疏层）。
	Tier string
	// EnvVar 非空时，这个槽位通过环境变量落地（claude 走这条）。
	EnvVar string
	// Desc 人类可读说明，给 explain 视图和 TUI 用。
	Desc string
}

// TaskCreate 一个被接管的 CLI。
type Agent struct {
	ID      string
	Bin     []string // PATH 里找这些名字，按序
	Dialect string   // openai | anthropic
	Slots   []Slot

	// BaseURLEnv / AuthEnv 指向代理的两个变量（env 注入型 agent 用）。
	BaseURLEnv string
	AuthEnv    string
	// UnsetEnv 必须从子进程环境删掉的干扰变量。
	//
	// 这是「切了没生效」最常见的成因：用户 shell 里残留的老变量盖过我们
	// 注入的，而一切看起来都成功了。
	UnsetEnv []string

	// ConfigPaths 配置文件型 agent 的目标文件（相对 $XDG_CONFIG_HOME）。
	ConfigPaths []string

	Notes string
}

// Registry 内置 agent。
var Registry = map[string]*Agent{
	"claude": {
		ID:      "claude",
		Bin:     []string{"claude"},
		Dialect: "anthropic",
		// Claude Code 自己就有档位层：settings.json 的 "model" 选档位
		// （opus / sonnet / haiku / fable），下面这些 env 决定每个档位
		// 指向什么模型。所以我们**完全不碰 settings.json 的 model 字段**，
		// 只重定义档位含义。
		Slots: []Slot{
			{Name: "opus", Tier: "heavy", EnvVar: "ANTHROPIC_DEFAULT_OPUS_MODEL",
				Desc: "opus 档；Plan Mode 下的 opusplan"},
			{Name: "fable", Tier: "heavy", EnvVar: "ANTHROPIC_DEFAULT_FABLE_MODEL",
				Desc: "fable 档；也是第三方 provider 上自动回退的识别依据"},
			{Name: "sonnet", Tier: "mid", EnvVar: "ANTHROPIC_DEFAULT_SONNET_MODEL",
				Desc: "sonnet 档；Plan Mode 外的 opusplan"},
			{Name: "haiku", Tier: "light", EnvVar: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
				Desc: "haiku 档；后台功能"},
			{Name: "subagent", Tier: "mid", EnvVar: "CLAUDE_CODE_SUBAGENT_MODEL",
				Desc: "所有 subagent / agent team / workflow；设 inherit 可交还给各自解析"},
		},
		BaseURLEnv: "ANTHROPIC_BASE_URL",
		AuthEnv:    "ANTHROPIC_AUTH_TOKEN",
		// ANTHROPIC_API_KEY 与 AUTH_TOKEN 并存时优先级未定义，只留一个。
		UnsetEnv:    []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"},
		ConfigPaths: []string{"claude/settings.json"},
		Notes:       "settings.json 的 model 字段不动；档位含义由 env 决定",
	},

	"opencode": {
		ID:          "opencode",
		Bin:         []string{"opencode"},
		Dialect:     "openai",
		ConfigPaths: []string{"opencode/opencode.json", "opencode/oh-my-openagent.json"},
		// TODO(M1): intra-agent 槽位由发现流水线从 oh-my-openagent.json
		// 里枚举出来（19 个 agents + categories），而不是硬编码在这里。
		// 现在的 config_file 注入用档位启发式把它们归类到 tier
		// （docs/11 §3.4），下一步要让每个 intra-agent 可单独绑定。
		Slots: nil,
		Notes: "槽位由发现流水线枚举；见 runtime/injection/config_file.go",
	},
}

func Get(id string) (*Agent, bool) {
	a, ok := Registry[id]
	return a, ok
}

func Names() []string {
	out := make([]string, 0, len(Registry))
	for k := range Registry {
		out = append(out, k)
	}
	return out
}

// EnvSlots 通过环境变量落地的槽位。
func (a *Agent) EnvSlots() []Slot {
	var out []Slot
	for _, s := range a.Slots {
		if s.EnvVar != "" {
			out = append(out, s)
		}
	}
	return out
}

// BuildEnv 生成要注入子进程的环境变量。
//
// 每个槽位的值是它的 **tier 名**，代理收到后再解析成真实模型——
// 这就是语义命名层：agent 侧永远看不到真实模型名。
func (a *Agent) BuildEnv(port int, authToken string) map[string]string {
	env := map[string]string{}
	if a.BaseURLEnv != "" {
		// 路径里带 agent 名，代理据此知道请求来自哪个 agent，
		// 从而应用该 agent 自己的 profile（per-agent 切换）。
		env[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s", port, a.ID)
	}
	if a.AuthEnv != "" {
		env[a.AuthEnv] = authToken
	}
	for _, s := range a.EnvSlots() {
		env[s.EnvVar] = s.Tier
	}
	return env
}

// FindReal 在 PATH 里找到真正的可执行文件，**跳过 skipDir**（shim 目录），
// 否则 shim 会调用自己造成无限递归。
func (a *Agent) FindReal(skipDir string) (string, error) {
	skipDir = filepath.Clean(skipDir)
	for _, name := range a.Bin {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if dir == "" || filepath.Clean(dir) == skipDir {
				continue
			}
			p := filepath.Join(dir, name)
			if !isExec(p) {
				continue
			}
			// 再确认它不是指回我们自己的符号链接
			if real, err := filepath.EvalSymlinks(p); err == nil {
				if filepath.Dir(real) == skipDir ||
					strings.HasSuffix(filepath.Base(real), "newgate") {
					continue
				}
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("PATH 里找不到 %s（已跳过 shim 目录 %s）",
		strings.Join(a.Bin, "/"), skipDir)
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}
