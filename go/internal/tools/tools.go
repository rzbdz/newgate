package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool 一个被接管的 CLI 的描述符。数据为主，接新工具不改代码逻辑。
type Tool struct {
	ID      string
	Bin     []string // PATH 里找这些名字，按序
	Dialect string   // openai | anthropic —— 决定 shim 注入哪套变量
	// EnvMap 档位 → 环境变量名。值是 newgate 的档位名（heavy/mid/light/vision）。
	EnvMap map[string]string
	// BaseURLEnv / AuthEnv 指向代理的两个变量
	BaseURLEnv string
	AuthEnv    string
	// UnsetEnv 必须从子进程环境里删掉的干扰变量。
	// 这是「切了没生效」最常见的成因：用户 shell 里残留的老变量盖过我们注入的。
	UnsetEnv []string
	Notes    string
}

// Registry 内置工具。
var Registry = map[string]*Tool{
	"claude": {
		ID:      "claude",
		Bin:     []string{"claude"},
		Dialect: "anthropic",
		// Claude Code 自己就有语义档位层：settings.json 的 "model" 选档位
		// （opus / sonnet / haiku / fable），这些 env 决定每个档位指向什么模型。
		// 所以我们**完全不碰 settings.json 的 model 字段**，只重定义档位含义。
		EnvMap: map[string]string{
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "heavy",
			"ANTHROPIC_DEFAULT_FABLE_MODEL":  "heavy",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "mid",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "light",
			// 所有 subagent / agent team / workflow 用的模型。
			// 设成具体档位会覆盖 subagent 自己的 frontmatter；
			// 想让 subagent 各自决定就把它设成 inherit。
			"CLAUDE_CODE_SUBAGENT_MODEL": "mid",
		},
		BaseURLEnv: "ANTHROPIC_BASE_URL",
		AuthEnv:    "ANTHROPIC_AUTH_TOKEN",
		// ANTHROPIC_API_KEY 与 AUTH_TOKEN 并存时行为不确定，统一只留一个。
		UnsetEnv: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"},
		Notes:    "settings.json 的 model 字段不动；档位含义由 env 决定",
	},
}

func Get(id string) (*Tool, bool) {
	t, ok := Registry[id]
	return t, ok
}

func Names() []string {
	var out []string
	for k := range Registry {
		out = append(out, k)
	}
	return out
}

// BuildEnv 生成要注入子进程的环境变量。
// port 是代理端口，roleOverride 允许某次调用覆盖某个档位。
func (t *Tool) BuildEnv(port int, authToken string) map[string]string {
	env := map[string]string{
		t.BaseURLEnv: fmt.Sprintf("http://127.0.0.1:%d", port),
		t.AuthEnv:    authToken,
	}
	for k, role := range t.EnvMap {
		env[k] = role
	}
	return env
}

// FindReal 在 PATH 里找到真正的可执行文件，**跳过 skipDir**（我们的 shim 目录），
// 否则 shim 会调用自己造成无限递归。
func (t *Tool) FindReal(skipDir string) (string, error) {
	skipDir = filepath.Clean(skipDir)
	for _, name := range t.Bin {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if dir == "" {
				continue
			}
			if filepath.Clean(dir) == skipDir {
				continue // 跳过 shim 目录
			}
			p := filepath.Join(dir, name)
			if isExec(p) {
				// 再确认一次它不是指回我们自己的符号链接
				if real, err := filepath.EvalSymlinks(p); err == nil {
					if filepath.Dir(real) == skipDir {
						continue
					}
					if strings.HasSuffix(filepath.Base(real), "newgate") {
						continue
					}
				}
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("PATH 里找不到 %s（已跳过 shim 目录 %s）",
		strings.Join(t.Bin, "/"), skipDir)
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// LookPathOutside 供 doctor 用：报告真实可执行文件位置。
func LookPathOutside(name, skipDir string) string {
	t := &Tool{Bin: []string{name}}
	if p, err := t.FindReal(skipDir); err == nil {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}
