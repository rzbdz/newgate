package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 配置模块贡献给 web 界面的三样东西：**档位映射**（每个 profile 一张绑定编辑器）、
// **源文件**（硬核用户直接改文件那条 tab）、**全局开关**（默认 profile 这类）。
//
// 为什么由本模块贡献而不是让界面自己读：界面一旦自己读 store，它就必须知道
// profile 文件的字段、mappings 目录的布局、哪些文件带凭据不能写回——那样加一个
// 模块的界面就要改界面。这条规矩与 cli/extension 那边一字不差（见那里关于
// 「status 要显示非出厂态的开关点」的那段），只是这次的服务端是浏览器。
//
// **写的知识也在这里**（每个概念带 Apply）：改档位要「只动 roles 那一段、别的键
// 原样保留」，这件事只有知道 profile 文件长什么样的人做得对。

// registerView 把本模块的概念挂到界面上。没有界面（没装 web-dashboard 或者
// 那是个 CLI 进程）时什么都不做——配置照常工作，只是没有 web 入口。
//
// 登记时**一个文件都不读**：产出函数 `concepts` 要等到真的有人来看界面才跑。
// 理由是这套东西的正确性而不是性能——每条 `newgate …` 命令都会跑到这里，而其中
// 绝大多数没有人会打开界面（见 lib/view 的包注释）。
func registerView(v view.Service) (modules.Release, error) {
	return v.Register("config", concepts)
}

// concepts 是「此刻配置的样子」：state.json 里归本模块的那几个字段、每个 profile
// 一张绑定编辑器、每个源文件一条。
//
// 它每次被调用都重新读盘，所以界面上的刷新是真的刷新——CLI 刚建的档位文件会在
// 下一次快照里出现。单个东西读不出来（文件删了、JSON 坏了）不牵连同组的别人：
// 那一张卡片带 Broken 说明，其余照常。
func concepts() ([]view.Concept, error) {
	var out []view.Concept
	out = append(out, stateConcept())
	for _, name := range profileNames() {
		out = append(out, profileConcept(name))
	}
	out = append(out, fileConcepts()...)
	return out, nil
}

// brokenConcept 是「这东西现在读不出来」的那张卡片：身份照报（前端才知道少了
// 谁），数据没有，也写不回去。
//
// 为什么不干脆不报这个概念：用户会以为那个档位不存在，然后去别处找。为什么不让
// 整次快照失败：一个坏文件让整个界面白屏，代价远大于它。
func brokenConcept(id, kind, title string, err error) view.Concept {
	return view.Concept{ID: id, Kind: kind, Title: title, Broken: err.Error()}
}

// ---------- 档位映射 ----------

type bindingData struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

type roleData struct {
	ID       string        `json:"id"`
	Bindings []bindingData `json:"bindings"`
}

type providerChoice struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
	KeyEnv   string   `json:"key_env,omitempty"`
	HasKey   bool     `json:"has_key"`
	Models   []string `json:"models"`
}

// profileData 是绑定编辑器的全部素材。
//
// 带上 providers 是刻意的：**候选是「provider × model」的组合**，界面要让人用
// 下拉框选而不是凭记忆敲字符串。这份清单从配置里来（各家声明过的模型），不问
// 上游——探活是 `newgate probe` 的事，界面加载不该去碰网络。
type profileData struct {
	Profile     string           `json:"profile"`
	File        string           `json:"file"` // 相对配置根；保存时原样回传
	Base        string           `json:"base"` // 基线（内容哈希），见 store.WriteIfUnchanged
	Description string           `json:"description,omitempty"`
	Default     bool             `json:"default"`
	Pinned      bool             `json:"pinned,omitempty"`
	Excluded    bool             `json:"excluded,omitempty"`
	Extends     string           `json:"extends,omitempty"`
	Roles       []roleData       `json:"roles"`
	Providers   []providerChoice `json:"providers"`
}

func profileConcept(name string) view.Concept {
	conceptID := "config.profile." + name
	file, err := profileFile(name)
	if err != nil {
		return brokenConcept(conceptID, view.KindMapping, name, err)
	}
	// 读**未合并**的原文：对一个紧凑的派生声明（extends）改一个字段，不该把它
	// 展开成全量——那会让用户在界面上点一下保存，文件就膨胀十倍。
	pr, err := store.LoadProfileRaw(name)
	if err != nil {
		return brokenConcept(conceptID, view.KindMapping, name, err)
	}
	// provider 表读不出来不算这张卡片坏掉：档位本身是好的、可以改，只是候选
	// 下拉框没有素材（providers.json 还没建是全新安装的正常状态）。这条以前是
	// 「整个 profile 静默消失」，现在是「照常显示、候选为空」。
	snap, snapErr := store.Load()
	if snapErr != nil {
		snap = &store.Snapshot{}
	}
	data := profileData{
		Profile: name, File: relToRoot(file), Base: store.Revision(file),
		Description: pr.Description, Pinned: pr.Pinned, Excluded: pr.Excluded, Extends: pr.Extends,
		Default:   snap.State != nil && snap.State.DefaultProfile == name,
		Providers: providerChoices(snap),
	}
	for _, id := range roleOrder(pr) {
		rd := roleData{ID: id}
		for _, b := range pr.Roles[id] {
			rd.Bindings = append(rd.Bindings, bindingData{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
		}
		data.Roles = append(data.Roles, rd)
	}
	title := name
	if pr.Description != "" {
		title = name + " — " + pr.Description
	}
	return view.Concept{
		ID: conceptID, Kind: view.KindMapping, Title: title,
		Data: data,
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyProfileRoles(conceptID, file, edit, base)
		},
	}
}

// roleOrder 是档位的展示顺序：四个标准档位按阶梯先后（重→轻），其余（含 "*" 与
// 客户端注册的动态键）按名字排在后面。
//
// 顺序不是排版洁癖：**档位是一条阶梯**，界面上按 heavy→light 排，用户一眼能看出
// 「这条链在往下走」；按字母序排出来的是 h/l/m/n，看的人得自己拼。
func roleOrder(pr *domain.Profile) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range domain.Roles {
		if _, ok := pr.Roles[id]; ok {
			out = append(out, id)
			seen[id] = true
		}
	}
	var rest []string
	for id := range pr.Roles {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func providerChoices(snap *store.Snapshot) []providerChoice {
	if snap.Providers == nil {
		return nil
	}
	// 模型清单从**各 profile 的绑定**里收集：那是「这家用过的模型」，比 providers.json
	// 里声明的 models 更贴近实际（而且不依赖那个字段存不存在）。
	used := map[string]map[string]bool{}
	for _, p := range snap.Profiles {
		for _, cands := range p.Roles {
			for _, b := range cands {
				if b.Provider == "" || b.Model == "" {
					continue
				}
				if used[b.Provider] == nil {
					used[b.Provider] = map[string]bool{}
				}
				used[b.Provider][b.Model] = true
			}
		}
	}
	names := make([]string, 0, len(snap.Providers.Providers))
	for name := range snap.Providers.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]providerChoice, 0, len(names))
	for _, name := range names {
		p := snap.Providers.Providers[name]
		pc := providerChoice{
			Name: name, Protocol: p.Protocol, BaseURL: p.BaseURL, KeyEnv: p.APIKeyEnv,
			HasKey: p.APIKey != "" || p.APIKeyEnv != "",
		}
		for m := range used[name] {
			pc.Models = append(pc.Models, m)
		}
		sort.Strings(pc.Models)
		out = append(out, pc)
	}
	return out
}

// applyProfileRoles 只改 roles 那一段，其余字段原样保留。
//
// 为什么不是「把整个 profile 序列化回去」：界面展示的是**概念**（档位、候选），
// 而文件里还有它不认识的东西（extends、window、compact、将来加的字段）。整份重写
// 等于拿界面的知识覆盖文件，用户手写的字段会在他没碰过的地方消失。
func applyProfileRoles(conceptID, file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Roles map[string][]bindingData `json:"roles"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the tier bindings in this request are not readable: {err}", i18n.A{"err": err})
	}
	roles := map[string]domain.Candidates{}
	for id, list := range patch.Roles {
		// 空列表**保留**（写进去一个空档位），不当作「删掉这个键」：界面把一个档位
		// 的候选全删光，意思是「这一档没有候选」——那与「没写这一档」（于是往上
		// 继承）是两件事，替用户选后者等于改了他没碰过的语义。
		cands := make(domain.Candidates, 0, len(list))
		for _, b := range list {
			cands = append(cands, domain.Binding{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
		}
		roles[id] = cands
	}

	if strings.HasSuffix(file, ".kv") {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		pr, err := store.ParseProfileKV(string(b))
		if err != nil {
			return "", err
		}
		pr.Roles = roles
		return writeThrough(conceptID, file, base, []byte(store.SerializeProfileKV(pr)))
	}

	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// map[string]json.RawMessage：只替换 roles 那一个键，别的键连**解析都不解析**，
	// 于是它们不可能被这次保存改变形状（数字的写法、键的顺序、未知字段）。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(roles)
	if err != nil {
		return "", err
	}
	doc["roles"] = raw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return writeThrough(conceptID, file, base, append(out, '\n'))
}

// writeThrough 是三个应用者共用的出口：落盘，并把「基线对不上」翻译成**界面的
// 冲突形状**（两边原文都在）。
//
// 翻译放在这里而不是 store 里：store 是文件层，它只该说「你手里那份过期了」，
// 而「过期了该怎么办」是界面与用户之间的事（把两份摊开让人选）。中间这一层
// 是拥有这些文件的那位——它知道文件叫什么、概念是谁。
func writeThrough(conceptID, file, base string, data []byte) (string, error) {
	rev, err := store.WriteIfUnchanged(file, base, data)
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return "", &view.Conflict{
			Concept: conceptID, Path: relToRoot(file),
			Base: stale.Base, Current: stale.Current,
			Yours: string(data), Theirs: string(stale.Disk),
		}
	}
	return rev, err
}

// ---------- 全局开关 ----------

type toggleItem struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // select | switch
	Value   string   `json:"value,omitempty"`
	On      bool     `json:"on,omitempty"`
	Options []string `json:"options,omitempty"`
	Why     string   `json:"why,omitempty"` // 一句话说明这个开关影响什么
}

type stateData struct {
	File  string       `json:"file"`
	Base  string       `json:"base"`
	Items []toggleItem `json:"items"`
}

// stateConcept 是 state.json 里**归 config 的那几个字段**。
//
// 只报自己的字段（别的模块的段各有其人，见 confighook 的登记表）：一个模块把
// state.json 整个端上去让用户改，等于让界面替所有模块做决定。
func stateConcept() view.Concept {
	file := paths.StateFile()
	snap, _ := store.Load()
	active := ""
	if snap != nil && snap.State != nil {
		active = snap.State.DefaultProfile
	}
	names := profileNames()
	data := stateData{
		File: relToRoot(file), Base: store.Revision(file),
		Items: []toggleItem{{
			ID: "default_profile", Kind: "select", Value: active, Options: names,
			Label: i18n.T("Default profile", nil),
			Why:   i18n.T("Which profile the chain starts from when no agent-specific one is set.", nil),
		}},
	}
	return view.Concept{
		ID: "config.state", Kind: view.KindToggles, Title: i18n.T("Global settings", nil),
		Data: data,
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyStateDefaultProfile("config.state", file, edit, base)
		},
	}
}

func applyStateDefaultProfile(conceptID, file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		DefaultProfile *string `json:"default_profile"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the settings in this request are not readable: {err}", i18n.A{"err": err})
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// RawMessage 的理由同 applyProfileRoles：state.json 里住着好几个模块各自的段，
	// 整份重写会拿这一处的知识覆盖别人的。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if patch.DefaultProfile != nil {
		raw, err := json.Marshal(*patch.DefaultProfile)
		if err != nil {
			return "", err
		}
		doc["default_profile"] = raw
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return writeThrough(conceptID, file, base, append(out, '\n'))
}

// ---------- 源文件 ----------

type fileData struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Text     string `json:"text"`
	// Redacted：这份内容是**脱敏过**的（凭据被换成了 ***），所以它只读——
	// 写回去就是把 *** 落盘，那是数据丢失，比不能编辑严重得多。
	Redacted bool `json:"redacted,omitempty"`
}

func fileConcepts() []view.Concept {
	var out []view.Concept
	add := func(path, language string) {
		rel := relToRoot(path)
		b, err := os.ReadFile(path)
		if err != nil {
			// 读不出来就报一张坏卡片（而不是不报）：源文件那条 tab 少一个文件名，
			// 用户以为这文件不存在，而它可能只是权限不对。
			out = append(out, brokenConcept("config.file."+rel, view.KindCode, rel, err))
			return
		}
		text, redacted := redact(string(b), language)
		c := view.Concept{
			ID: "config.file." + rel, Kind: view.KindCode, Title: rel,
			Data: fileData{Path: rel, Language: language, Text: text, Redacted: redacted},
		}
		if !redacted {
			c.Apply = func(edit json.RawMessage, base string) (string, error) {
				var patch struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(edit, &patch); err != nil {
					return "", i18n.Ef(err, "the file content in this request is not readable: {err}", i18n.A{"err": err})
				}
				return writeThrough(c.ID, path, base, []byte(patch.Text))
			}
		}
		out = append(out, c)
	}
	add(paths.ProvidersFile(), "json")
	add(paths.StateFile(), "json")
	if ents, err := os.ReadDir(paths.Mappings()); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			switch {
			case strings.HasSuffix(name, ".kv"):
				add(filepath.Join(paths.Mappings(), name), "kv")
			case strings.HasSuffix(name, ".json"):
				add(filepath.Join(paths.Mappings(), name), "json")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// secretKeys 是**绝不外发**的字段名。
//
// 显式列而不是「反正只挑我认识的字段」：源文件那条 tab 是把整个文件发给浏览器的，
// 而 providers.json 里的 api_key、state.json 里的 control_token 都是凭据。界面
// 监听在 loopback 上，但 loopback 边界意味着**本机任何进程**都能读——凭据不该
// 只靠这一点保护。
var secretKeys = map[string]bool{
	"api_key": true, "control_token": true, "root_key": true, "token": true,
}

func redact(text, language string) (string, bool) {
	if language == "json" {
		var doc map[string]any
		if json.Unmarshal([]byte(text), &doc) != nil {
			return text, false // 解析不了就原样给出去（只读视图，不写回）
		}
		if !scrub(doc) {
			return text, false
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return text, true
		}
		return string(b) + "\n", true
	}
	found := false
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && secretKeys[strings.TrimSpace(key)] {
			lines[i] = key + "=***"
			found = true
		}
	}
	return strings.Join(lines, "\n"), found
}

func scrub(v any) bool {
	touched := false
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if secretKeys[k] {
				node[k] = "***"
				touched = true
				continue
			}
			if scrub(val) {
				touched = true
			}
		}
	case []any:
		for _, item := range node {
			if scrub(item) {
				touched = true
			}
		}
	}
	return touched
}

// ---------- 小工具 ----------

func profileNames() []string {
	names, err := store.ListProfiles()
	if err != nil {
		return nil
	}
	return names
}

// profileFile 是这个 profile 在磁盘上的真身（.kv 优先，与 store 的读取次序一致）。
func profileFile(name string) (string, error) {
	kv := filepath.Join(paths.Mappings(), name+".kv")
	if _, err := os.Stat(kv); err == nil {
		return kv, nil
	}
	js := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(js); err == nil {
		return js, nil
	}
	return "", fmt.Errorf("profile %s has no file", name)
}

func relToRoot(path string) string {
	rel, err := filepath.Rel(paths.Root(), path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}
