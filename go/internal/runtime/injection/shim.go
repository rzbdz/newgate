package injection

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
)

const (
	beginMark = "# >>> newgate >>>"
	endMark   = "# <<< newgate <<<"
)

// Dir 存放 shim 的目录。它会被前置到 PATH。
func Dir() string { return filepath.Join(paths.Config(), "bin") }

// Install 为某个工具建 shim：Dir()/<tool> 指向 newgate 自己。
//
// 为什么用 PATH shim 而不是写工具的配置文件：
// 用户的 shell rc 里往往已经 export 了 ANTHROPIC_BASE_URL / AUTH_TOKEN，
// 而 shell 环境的优先级高于 settings.json 的 env 块——写配置文件会被静默
// 盖掉，正是最难查的那类故障。shim 在子进程里显式设置 env，能盖过一切。
func Install(toolID string) (string, error) {
	if _, ok := agents.Get(toolID); !ok {
		return "", fmt.Errorf("不认识的工具 %q（已知：%s）",
			toolID, strings.Join(agents.Names(), ", "))
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return "", err
	}
	link := filepath.Join(Dir(), toolID)
	_ = os.Remove(link)
	if err := os.Symlink(self, link); err != nil {
		return "", err
	}
	return link, nil
}

func Uninstall(toolID string) error {
	return os.Remove(filepath.Join(Dir(), toolID))
}

// Installed 列出已装的 shim。
func Installed() []string {
	ents, err := ioutil.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// RCFiles 需要加 PATH 的 shell 启动文件（只返回已存在的）。
func RCFiles() []string {
	home := paths.Home()
	var out []string
	for _, f := range []string{".zshrc", ".bashrc", ".bash_profile", ".profile"} {
		p := filepath.Join(home, f)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func block() string {
	return fmt.Sprintf("%s\n# newgate 的 shim 目录必须在 PATH 最前面，才能拦住 claude 等命令。\n"+
		"# 删掉这一段（或跑 newgate shim off）即可完全恢复原状。\n"+
		"export PATH=\"%s:$PATH\"\n%s\n", beginMark, Dir(), endMark)
}

// HasBlock 该 rc 文件里有没有我们的块。
func HasBlock(path string) bool {
	b, err := ioutil.ReadFile(path)
	return err == nil && strings.Contains(string(b), beginMark)
}

// AddToRC 往 rc 文件追加 PATH 块。幂等。原文件先备份。
// 只**追加**，不改动用户已有的任何一行——包括他自己的 ANTHROPIC_* export
// （那些会被 shim 在子进程里覆盖，不需要动）。
func AddToRC(path string) (changed bool, err error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return false, err
	}
	if strings.Contains(string(b), beginMark) {
		return false, nil
	}
	if err := backupRC(path, b); err != nil {
		return false, err
	}
	out := string(b)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "\n" + block()
	return true, writeAtomicMode(path, []byte(out), 0o644)
}

// RemoveFromRC 精确删除我们的块，其余字节不动。
func RemoveFromRC(path string) (changed bool, err error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return false, err
	}
	s := string(b)
	i := strings.Index(s, beginMark)
	if i < 0 {
		return false, nil
	}
	j := strings.Index(s[i:], endMark)
	if j < 0 {
		return false, fmt.Errorf("%s 里有起始标记但没有结束标记，不敢自动改，请手工删除 %s 之后的那几行",
			path, beginMark)
	}
	end := i + j + len(endMark)
	// 连带吃掉紧随其后的换行
	for end < len(s) && s[end] == '\n' {
		end++
	}
	// 连带吃掉块前面我们自己加的那个空行
	start := i
	for start > 0 && s[start-1] == '\n' {
		start--
		if start > 0 && s[start-1] != '\n' {
			break
		}
	}
	if err := backupRC(path, b); err != nil {
		return false, err
	}
	return true, writeAtomicMode(path, []byte(s[:start]+"\n"+s[end:]), 0o644)
}

func backupRC(path string, content []byte) error {
	dir := filepath.Join(paths.BackupDir(), "rc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(dir, filepath.Base(path)+".bak"), content, 0o600)
}

// writeAtomicMode 带权限位的原子写。config_file.go 里的 writeAtomic
// 固定 0600（那是配置文件），rc 文件需要 0644。
func writeAtomicMode(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".newgate.tmp"
	if err := ioutil.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// InPath 当前进程的 PATH 里有没有我们的 shim 目录，且在真实工具之前。
func InPath() bool {
	want := filepath.Clean(Dir())
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(d) == want {
			return true
		}
	}
	return false
}
