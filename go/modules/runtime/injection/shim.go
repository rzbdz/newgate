package injection

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate/go/modules/config/paths"
	"github.com/rzbdz/newgate/go/modules/runtime/agentstate"
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
	agents := agentstate.Catalog()
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
	agents := agentstate.Catalog()
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
	agents := agentstate.Catalog()
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
	agents := agentstate.Catalog()
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