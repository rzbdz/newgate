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

// Uninstall 摘掉一个 shim。两道闸：名字必须是已知 agent，链接必须是我们装的。
// 同名的真实可执行文件、用户手工留的 claude-bak、自己写的包装脚本一律不动
// ——宁可留着也不能误删，那是用户的应急手段。想看它们用 Foreign()。
func Uninstall(toolID string) error {
	if _, ok := agents.Get(toolID); !ok {
		return fmt.Errorf("%q 不是已知 agent，没动它（shim 目录里的其他东西不属于 newgate）", toolID)
	}
	link := filepath.Join(Dir(), toolID)
	if _, err := os.Lstat(link); err != nil {
		return err
	}
	if !isOurs(link) {
		return fmt.Errorf("%s 不是 newgate 装的链接，没动它", link)
	}
	return os.Remove(link)
}

// Installed 列出**我们自己装的** shim。
//
// 只认「名字是已知 agent、且是指向 newgate 二进制的符号链接」的条目。
// 为什么要这么严：这个列表会被 newgate stop 拿去逐个删除。目录里可能有
// 用户手工重命名的 claude-bak、自己塞的脚本——那些不是我们的东西，
// 既不能算进「已接管」，更不能被我们删掉。想看它们用 Foreign()。
func Installed() []string {
	ents, err := ioutil.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if _, ok := agents.Get(e.Name()); !ok {
			continue
		}
		if !isOurs(filepath.Join(Dir(), e.Name())) {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// Foreign 列出 shim 目录里不是我们装的条目。展示给用户，但我们不动它们。
func Foreign() []string {
	ents, err := ioutil.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if _, ok := agents.Get(name); ok && isOurs(filepath.Join(Dir(), name)) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// isOurs 这个路径是不是**我们装的**符号链接。
//
// 两条判据，满足其一即可：
//   - 指向当前这个可执行文件（刚装的 shim 必然如此，最可靠）
//   - 目标名里带 newgate（二进制被换了位置/升级过，链接还指向老路径）
//
// 为什么不只看名字：用户完全可能把二进制装成别的名字，那时按名字判断会
// 把自己装的链接认成外人，stop 就摘不掉它——又回到「stop 了还在走 newgate」。
func isOurs(link string) bool {
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	dst, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if self, err := os.Executable(); err == nil && sameFile(dst, self) {
		return true
	}
	return strings.Contains(filepath.Base(dst), "newgate")
}

// sameFile 两个路径是否指向同一个文件（跟着符号链接走，解决 /var 与
// /private/var 这类等价路径）。
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
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
		"# 删掉这一段（或跑 newgate shim uninstall）即可完全恢复原状。\n"+
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
