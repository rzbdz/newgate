// Package extmanifest 读并落实**模块发行版**：这次构建装的是哪个发行版、
// 那个发行版要哪些模块。
//
// # 为什么要有它
//
// `modules/` 里的是本发行版自带的模块，清单由构建期扫描得到（tools/genmodules）。
// 但「我要装哪些模块」是**发行版**的事，不是扫描器能猜的：同一份内核可以被装成
// 好几种产品，各自挑不同的模块（有的还功能重叠、互相替代）。
//
// # 两张纸，两个主人
//
// 发行版这件事天然有两个问题，答案属于不同的人，所以分成两个文件：
//
//	"我这次构建用哪个发行版？"        → 核心仓库根的 modules-ext.json（Pin）
//	"这个发行版由哪些模块组成？"      → 发行版仓库里的 dist.json（Spec）
//
// 第一张是**构建者**的（我 clone 了核心，我想装哪个发行版），第二张是**发行版
// 作者**的（我的产品包含什么）。合成一张纸的后果是一个具体的故事：有人 fork 了
// newgate-ext 想做自己的发行版，加了自己的模块——然后发现「启用哪几个模块」这条
// 配置在**核心仓库**里，他 fork 的那个仓库改了不生效，于是他还得去改核心仓库的
// 文件。fork 一下就能拥有一个发行版，前提就是这张纸躺在被 fork 的那个仓库里。
//
// # 为什么是「配置里拷代码」而不是 git submodule
//
// 2026-09-20 之前的实现把 `go/modules-ext` 做成 git submodule。它的毛病不是不好用，
// 是**把发行版的选择写进了仓库的版本控制**：
//
//   - 一份 checkout 只能钉一个 submodule 地址，「同一份源码换一个发行版」就变成了
//     改 `.gitmodules` 再提交——而用户根本不该有权改主仓库的版本控制内容；
//   - submodule 的 URL 是 `git@…`（ssh），**没有 key 的人 clone 不了主仓库**：
//     一个只想跑默认发行版的人被牵连了；
//   - 想同时试两种发行版就得两份 worktree（submodule 是每个工作区一份状态）。
//
// 改成「声明里写仓库地址，构建期 clone 到本机」之后：`modules-ext.json` 是一个
// 普通文件，submodule 一步都不需要——用户 clone 主仓库、编辑那份 json、跑 `make
// generate`，代码自己就下来了。`go/modules-ext/` 是**产物**，进 .gitignore。
//
// # 为什么本地放得下这么重的一步（构建期主动出网 clone）
//
// 它换掉的是「用户手工 clone 到一个**固定**路径」这条前提。那条前提的代价不是
// 麻烦，是**它不在代码里**：一根 Pin 说「装 ext 的 X 模块」，而模块到底在不在、
// 是不是那一版，全靠用户记得做对。构建期自己拉就没有这层运气——同一个 commit +
// 同一份 Pin 在任何机器上装出同一个产品。
//
// 代价说清楚，两条：
//
//   - **离网构建需要本机已有 checkout**：`NEWGATE_EXT_DRYRUN=1` 时不再拉代码，
//     checkout 从哪来由构建方负责（CI 与打包机就是这么用的）。
//   - **Pin 里钉的版本必须是远端可达的**（完整提交号、tag、分支名都行）。写一个
//     只存在于某人本地的 hash，别人的构建装不出来——那正是要抓的错。
package extmanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FileName 是声明文件的名字，放在**仓库根**（`go/` 的上一级）。
const FileName = "modules-ext.json"

// DefaultRepo 是默认发行版的外部模块仓库。
//
// 声明里 repo 留空 = 用它。这样「换一个发行版」只需要改 json 里那一行，而不是
// 每个想跑默认发行版的人都要把地址抄一遍。
const DefaultRepo = "https://github.com/rzbdz/newgate-modules-ext.git"

// Pin 是**核心仓库**根上那份 `modules-ext.json`：这次构建用哪个发行版。
//
// 它是构建者（或发行版自己的 build 脚本）写的一张纸，字段刻意少——只回答
// 「去哪拿、拿哪一版、那家的规格书叫什么名字」。
type Pin struct {
	// Repo 发行版仓库的地址。留空 = DefaultRepo。
	Repo string `json:"repo"`

	// Revision 要的版本：完整提交号、tag、分支名都行（交给 git 解析）。
	// 空 = 远端默认分支的 HEAD，只适合试装——Pin 是要进版本控制的，钉住才算数。
	Revision string `json:"revision"`

	// Distribution 是解析出来的发行版名（从规格书读回，只用于日志与标记文件）。
	// 它不在 `modules-ext.json` 里写——写了就有两个真相。
	Distribution string `json:"-"`

	// Spec 规格书在**发行版仓库里**的路径，默认 dist.json。
	// 留一个口是因为发行版可能有多个变体（`dist-min.json` / `dist-full.json`），
	// 而那属于发行版自己的目录结构，不该由核心来规定。
	Spec string `json:"spec"`
}

func (p *Pin) fillDefaults() {
	if p.Repo == "" {
		p.Repo = DefaultRepo
	}
	if p.Spec == "" {
		p.Spec = SpecFileName
	}
}

// Spec 是**发行版仓库**根上那份 `dist.json`：这个发行版由哪些模块组成。
//
// 它是发行版作者写的一张纸。**fork 一个 ext 仓库就该拥有一个自己的发行版**——
// 前提正是这张纸躺在他 fork 的那个仓库里，而不是在核心仓库里。
type Spec struct {
	// Distribution 本发行版的名字。它进装配日志与 `newgate plugin`，用来回答
	// 「这份二进制是哪个产品」——同一台机器上装两份发行版时，这是唯一的区分。
	Distribution string `json:"distribution"`

	// Modules 启用哪些外部模块（取发行版仓库里的**目录名**）。没点名的目录一律
	// 不装——「clone 下来就自动全装上」会让升级一个模块变成升级全部。
	Modules []string `json:"modules"`

	// Disable 关掉哪些**核心仓库自带**的模块（`go/modules/` 下的目录名）。
	//
	// 为什么需要它，而不是「不想要就删目录」：功能重叠是真实存在的
	// （modules/cli 与发行版的 simple-cli 就是同一个位置的两个实现），而「删目录」
	// 不是一条可声明的构建事实——它只存在于某个人的工作区里，别人的机器上装出来
	// 的东西不一样。写进规格书之后，同一份核心源码 + 同一份规格书在任何机器上都
	// 装出同一个产品。
	//
	// 关掉一个模块**不需要它是可选的**：依赖图早已按 Optional/Need 区分了强弱，
	// 关掉一个被人 Need 的模块会在构图期当场失败（那是正确的反应——说明这份规格书
	// 自相矛盾）。这和「用户摘掉一个必装模块」是同一件事，见 app/matrix_test.go。
	Disable []string `json:"disable"`

	// Core 是「这个发行版基于内核的哪一版」。
	//
	// **核心这一侧不读它**，读它的是发行版自己的构建脚本（build/release.sh 按它
	// clone 内核源码），但这不等于核心可以不声明它：规格书是严格模式（未知键当场
	// 报错，见 decode），不声明就意味着任何写了 core 的规格书都读不进来。
	//
	// 为什么这条「反向的钉子」值得存在：两个仓库互相钉住对方的版本，各自回答
	// 「我这一版是配着谁的哪一版造的」。少了它，发行版的构建脚本只能把内核地址与
	// 版本写死在脚本里——而那是发行版的事实，不是脚本的实现细节。
	Core *CoreRef `json:"core,omitempty"`
}

// CoreRef 指向内核仓库的某一版。
type CoreRef struct {
	Repo     string `json:"repo"`
	Revision string `json:"revision"`
}

// SpecFileName 是规格书在发行版仓库里的默认文件名。
const SpecFileName = "dist.json"

// Manifest 是一次构建的完整结论：一张 Pin + 它指向的那份 Spec。
//
// 它是**派生值**，不是文件：两个来源分别是 Pin（核心仓库根）与 Spec（发行版
// 仓库根）。下游（genmodules、`newgate plugin`）只认它，不必知道背后是两张纸。
type Manifest struct {
	Pin  Pin
	Spec Spec

	// Resolved 是这次实际用的提交（Load 之后才有）。
	Resolved string
}

// 两个便利访问器，让下游读起来像在读一份东西。
func (m *Manifest) Distribution() string { return m.Spec.Distribution }
func (m *Manifest) Modules() []string    { return m.Spec.Modules }

// ErrNoManifest 表示这份源码没有模块发行版声明（合法状态）。
var ErrNoManifest = errors.New("extmanifest: 没有 " + FileName)

// ErrOffline 表示需要联网但取不到远端仓库。
//
// 单独一个错误类型是为了让调用方能给出**可执行**的提示，而不是把一次网络故障
// 报成「声明是错的」。
var ErrOffline = errors.New("extmanifest: 取不到远端仓库")

// Path 返回**Pin** 的路径（核心仓库根）。
//
// `NEWGATE_MODULES_PIN` 可以指到另一份 Pin 上——**这是给测试与发行版自己的构建
// 脚本用的**：「换个发行版编一版看看」（比如关掉 modules/cli、启用 ext 的
// simple-cli）不该要求改核心仓库里那份默认 Pin，那样每次试验都会留下一处待还原
// 的改动。发行版的 build 脚本更应该指自己的 Pin：核心仓库根那份是**默认发行版**
// 的配置，不是所有发行版的。
//
// 它刻意是**环境变量而不是命令行开关**：读它的是构建期工具（make generate），而
// 构建命令的形状已经定了；多一个开关意味着 Makefile、CI、文档三处都要跟着改。
// 注意它只换「读哪张 Pin」，**不换 checkout 的位置**——位置由 Dir 唯一决定。
func Path(repoRoot string) string {
	if v := os.Getenv("NEWGATE_MODULES_PIN"); v != "" {
		return v
	}
	return filepath.Join(repoRoot, FileName)
}

// Load 读「核心仓库的 Pin + 它指向的发行版规格书」，合成一次构建的结论。
//
// 第二个返回值表示 Pin 是否存在——**缺席是合法状态**（没有发行版的构建照常跑），
// 所以它不是错误。Pin 在但规格书不在是**错误**：一次只说了去哪拿、没说拿什么的
// 构建，唯一的下场是装出一个和谁的声明都对不上的产品。
func Load(repoRoot string) (*Manifest, bool, error) {
	raw, err := os.ReadFile(Path(repoRoot))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var pin Pin
	if err := decode(Path(repoRoot), raw, &pin); err != nil {
		return nil, true, err
	}
	pin.fillDefaults()

	head, spec, err := Ensure(repoRoot, &pin)
	if err != nil {
		return nil, true, err
	}
	sort.Strings(spec.Modules)
	return &Manifest{Pin: pin, Spec: spec, Resolved: head}, true, nil
}

// decode 读一段 JSON，未知键当场报错（拼错的键静默忽略过一次，就再也不会有人发现）。
func decode(what string, raw []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// Dir 返回外部模块仓库在本机的 checkout 位置。
//
// 它在 **go/ 里面**（`go/modules-ext/`），不在仓库根：外部模块必须是**同一个 Go
// module 里的包**才能被编进来。放到仓库根就得给 ext 单独一份 go.mod，再在主
// go.mod 里写 `replace ../modules-ext` —— 那会让「装一个模块 = 有目录 + 重新编译」
// 这条规矩多出两个前提，而 `replace` 是**构建方**的东西，换台机器、换个发行方式
// 就失效。
//
// 放在 go/ 里的第二个好处：ext 模块与 modules/ 里的模块形态完全相同（同一个
// import 前缀、同一张依赖图、同一个扫描判据），所以「把一个 ext 模块直接拷进
// modules/」也能工作——两条路的区别只是**谁决定装**，不是**怎么装**。
//
// 因为它是产物（构建期 clone 出来的），它在 .gitignore 里，**不参与版本控制**。
func Dir(repoRoot string) string {
	return filepath.Join(repoRoot, "go", ModulesExtDirName)
}

// ModulesExtDirName 是外部模块仓库的目录名（相对 go/）。
const ModulesExtDirName = "modules-ext"

// ModulesSubdir 是模块在**发行版仓库里**的存放目录（`<repo>/modules/<名字>/module.go`）。
//
// 与内核的 `go/modules/` 同名不是巧合：一份发行版仓库里除了模块还有别的（规格书、
// 构建脚本、发行版自己的测试），模块得有自己的一格。同名的好处是「把内核的某个
// 模块搬进发行版」或反过来，路径心智模型一个都不用改。
const ModulesSubdir = "modules"

// ModuleRel 返回某个外部模块相对 `go/` 的路径（`modules-ext/modules/<名字>`）。
//
// 它是**import 路径后缀**与磁盘路径共用的一份答案：两者分头拼字符串的话，
// 改了目录约定就会出现「文件找得到、编不过」这种半截失败。
func ModuleRel(name string) string {
	return path.Join(ModulesExtDirName, ModulesSubdir, name)
}

// DryRun 报告这次构建是不是**只校验不拉代码**。
//
// 给 CI、打包机、以及任何离网环境用：那时 checkout 是预先准备好并缓存的，构建
// 不该再去碰网络（CI 里见 .github/workflows/ci.yml）。默认（本地开发）是拉。
//
// 为什么默认拉：声明这个词的意思是「按它装」。如果默认不拉，用户改完 json 得
// 自己知道还要手工 clone 一次——那正是 submodule 时代的坏体验，绕了一圈回来。
func DryRun() bool {
	return os.Getenv("NEWGATE_EXT_DRYRUN") != ""
}

// Ensure 把一张 Pin 落实到磁盘：checkout 就位、HEAD 落在钉的版本上、规格书读进来，
// 最后把「这次构建装的是哪个发行版」写成 checkout 外侧的一行痕迹（writeTraces）。
//
// 顺序是刻意的：**痕迹在规格书之后**——发行版名来自规格书，它是那行痕迹里最有用
// 的一个字段（「这份二进制是哪个产品」），没有它这行就只剩一串提交号。
//
// 幂等：目录已在就复用，只做必要时的 fetch/checkout。
func Ensure(repoRoot string, pin *Pin) (head string, spec Spec, err error) {
	dir := Dir(repoRoot)
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil {
		if DryRun() {
			return "", Spec{}, fmt.Errorf("NEWGATE_EXT_DRYRUN 已开，但 %s 不是一个 checkout；\n"+
				"  离网构建需要先把 %s 拉到 %s", dir, pin.Repo, dir)
		}
		if err := clone(pin.Repo, dir); err != nil {
			return "", Spec{}, err
		}
	}
	head, err = git(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", Spec{}, fmt.Errorf("读 %s 的 HEAD 失败: %w", dir, err)
	}
	if pin.Revision != "" {
		if head, err = ensureRevision(dir, pin, head); err != nil {
			return "", Spec{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, pin.Spec))
	if err != nil {
		return "", Spec{}, fmt.Errorf("%s 里没有 %s：一份只说了去哪拿、没说拿什么的 Pin\n"+
			"  装不出任何东西（发行版仓库根上该有这么一份规格书）", dir, pin.Spec)
	}
	if err := decode(filepath.Join(dir, pin.Spec), raw, &spec); err != nil {
		return "", Spec{}, err
	}
	if spec.Distribution == "" {
		return "", Spec{}, fmt.Errorf("%s/%s: distribution 必填（它进装配日志，是「这份二进制是哪个产品」的唯一答案）",
			dir, pin.Spec)
	}
	pin.Distribution = spec.Distribution
	if err := writeTraces(repoRoot, dir, pin, head); err != nil {
		return "", Spec{}, err
	}
	return head, spec, nil
}

// ensureRevision 让 checkout 的 HEAD 落在声明钉的那个提交上。
func ensureRevision(dir string, pin *Pin, head string) (string, error) {
	want, err := resolveIn(dir, pin)
	if err != nil {
		return "", err
	}
	if head == want {
		return want, nil
	}
	if err := gitRun(dir, "checkout", "--quiet", want); err != nil {
		return "", fmt.Errorf("切到 %s 失败: %w", short(want), err)
	}
	return want, nil
}

// resolveIn 把声明里的 revision 解析成一个提交号；解析不了就问远端。
//
// **用 fetch 而不是 ls-remote**：GitHub 允许按完整提交号 fetch，而 `ls-remote`
// 对提交号一律回空（2026-09-20 实测：`git ls-remote <repo> <sha>` 无输出，而
// `git fetch <repo> <sha>` 直接拿到那个 commit）。声明主推的用法恰好是钉提交号，
// 所以这个区别不是细节。`git fetch <repo> <ref>` 对分支名与 tag 同样有效，
// 一条路就够。
//
// 本地已有那个提交就不出网（离网构建、以及上一轮构建留下的 checkout 走这条）——
// 短路只对**完整提交号**开：分支/tag 的本地值是快照，正是「声明说装 v2、实际装的
// 是 v1」要防的那种偏差。
func resolveIn(dir string, pin *Pin) (string, error) {
	if isFullSHA(pin.Revision) {
		if _, err := git(dir, "cat-file", "-e", pin.Revision+"^{commit}"); err == nil {
			return pin.Revision, nil
		}
	}
	if err := gitRun(dir, "fetch", "--quiet", pin.Repo, pin.Revision); err != nil {
		return "", fmt.Errorf("%w：git fetch %s %s 失败: %v（钉的版本必须人人取得到——"+
			"分支、tag，或完整提交号；只存在于某人本地的 hash 不算）", ErrOffline, pin.Repo, pin.Revision, err)
	}
	sha, err := git(dir, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("远端 %s 上没有 %q", pin.Repo, pin.Revision)
	}
	return sha, nil
}

// isFullSHA 判一个字符串是不是完整提交号（40 位十六进制）。
func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// writeTraces 落一份指纹：checkout **外侧**的标记文件。
//
// 刻意只写一份、且写在 checkout 外面（`go/.modules-ext`，与 modules-ext/ 同级）。
// 第一版往 checkout 里抄了一份规格书，两个问题：git 工作区被弄脏，而
// `ensureRevision` 里的 `git checkout` 会被工作区里的改动挡住（换版本当场失败）；
// 而且它是冗余的——规格书本来就在 checkout 里躺着。
//
// 标记文件是产物（.gitignore 里），内容是「这次构建装的是哪个发行版」的答案：
// 发行版名、哪个仓库、哪一版、解析成哪个提交、启用了哪些模块。
func writeTraces(repoRoot, dir string, pin *Pin, head string) error {
	marker := fmt.Sprintf(`# 这是构建期从发行版仓库拉下来的模块 checkout —— 产物，不进版本控制。
# Pin：%s
# 仓库：%s
# 规格书：%s
# 发行版：%s
# 请求的版本：%s
# 实际提交：%s
`, Path(repoRoot), pin.Repo, pin.Spec,
		pin.Distribution, orDefault(pin.Revision, "(远端默认分支)"), head)
	return os.WriteFile(filepath.Join(filepath.Dir(dir), markerName), []byte(marker), 0o644)
}

// markerName 是 checkout 的外侧标记文件（相对 go/）。
const markerName = ".modules-ext"

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// clone 把仓库拉到 dir。
func clone(repo, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("git", "clone", "--quiet", repo, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w：git clone %s %s 失败: %v\n%s", ErrOffline, repo, dir, err, out)
	}
	return nil
}

// Disables 报告某个自带模块是否被这份规格书关掉。
func (m *Manifest) Disables(name string) bool {
	for _, d := range m.Spec.Disable {
		if d == name {
			return true
		}
	}
	return false
}

// BuiltinDir 返回本发行版自带模块目录（go/modules）。
func BuiltinDir(repoRoot string) string { return filepath.Join(repoRoot, "go", "modules") }

// CheckDisable 校验 Disable 里点名的模块**确实存在**。
//
// 为什么这是一个错误而不是警告：拼错一个名字的后果是「我以为关掉了，其实它在
// 跑」——而功能重叠那一类问题恰恰是**静默**的（两个实现都装着，谁生效取决于
// 拓扑序），用户会以为自己已经换掉了。名字写错必须当场失败。
func CheckDisable(repoRoot string, m *Manifest) error {
	for _, name := range m.Spec.Disable {
		if _, err := os.Stat(filepath.Join(BuiltinDir(repoRoot), name, "module.go")); err != nil {
			return fmt.Errorf("声明里要关掉模块 %q，但 modules/%s/module.go 不存在（名字写错了？）", name, name)
		}
	}
	return nil
}

// git 在 dir 里跑一条 git 命令并返回去掉首尾空白的输出。
func git(dir string, args ...string) (string, error) {
	out, err := gitOutput(dir, args...)
	return strings.TrimSpace(out), err
}

// gitRun 同 git，但不要输出（只关心成败）。
func gitRun(dir string, args ...string) error {
	_, err := gitOutput(dir, args...)
	return err
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}
