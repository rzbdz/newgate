package gateway

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/gateway/metrics"
)

// 网关贡献给 web 界面的东西：**计数器**。
//
// 为什么是网关报而不是界面自己读 metrics.Default：那些计数器的名字、分组、
// 以及每一行「这个数意味着什么」的说法，全是数据面的语义（见 metrics/hints.go）。
// 界面自己去读那张全局表的话，它就得认识 `[shape-400]` 这类**机器标记**——
// 那是 grep 的锚点，不是给人看的文案。
//
// 这里与 CLI 那边同源：`newgate metrics` 用的也是这份分组与说法（同一个
// metrics.Group / metrics.Hint），所以两个界面上看到的东西不会各说各话。

type seriesEntry struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type seriesGroup struct {
	ID       string        `json:"id"`
	Label    string        `json:"label"`
	Counters []seriesEntry `json:"counters"`
}

type seriesData struct {
	Groups []seriesGroup `json:"groups"`
	Total  uint64        `json:"total"`
}

// metricsConcepts 是网关这一刻的展示面：计数器，以及代理日志的尾巴。
//
// 它被调用的时机是**有人来看界面**，不是装配——所以计数器是「现在」的，日志尾巴
// 也是「现在」的，而不是这个进程起来那一刻的（见 lib/view 的包注释）。
//
// 日志为什么由网关报而不是让界面自己去 tail 那个文件：日志的**行是什么**是数据面
// 的语义（`-> provider/model`、`X-Newgate-Route`、上游原文那几行）。界面自己去读
// 文件的话，它就得认识那些行——而它们是 grep 的锚点，不是给人看的文案（与
// metrics 那边同一条理由）。
func metricsConcepts() ([]view.Concept, error) {
	return []view.Concept{
		{
			ID: "gateway.metrics", Kind: view.KindSeries, Title: i18n.T("Counters", nil),
			Data: seriesGroups(),
		},
		logConcept(),
	}, nil
}

type logData struct {
	Path string `json:"path"`
	// Lines 是**从新到旧**还是从旧到新？从旧到新（打完的最后一行在最下面）：
	// 日志是往下读的，把最新一行放顶上会让每次刷新都像跳了一下。
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"`
	Missing   bool     `json:"missing,omitempty"`
}

// logTail 是读多少行。几百行足够看清「刚刚发生了什么」，而这个视图每隔几秒就会
// 被重新问一次——报一整个 16MB 的日志过去，是让浏览器每次刷新都背一遍历史。
const logTail = 400

// logRead 是一次最多读多少字节（从文件尾部倒着读）。
//
// 不按行数 seek：行的长度没有上界（一条上游报错原文可以很长），按行回溯就得把
// 整个文件读一遍。256KB 对「最后 400 行」是绰绰有余的余量，而它是个**上界**——
// 这个函数不会因为日志涨到 16MB 而变慢。
const logRead = 256 << 10

func logConcept() view.Concept {
	path := paths.LogFile()
	lines, truncated, err := tailLines(path, logTail, logRead)
	data := logData{Path: path, Lines: lines, Truncated: truncated}
	// 日志文件还没建（这个进程还没写过东西）不是错误，是**正常的第一天**。分清楚
	// 这两件事，界面才不会把「还没有日志」画成「出错了」。
	data.Missing = err != nil
	if data.Missing {
		data.Lines = nil
	}
	return view.Concept{
		ID: "gateway.log", Kind: view.KindLog, Title: i18n.T("Proxy log", nil),
		// 只读：日志是**发生过的事**，不是配置。这个概念没有 Apply。
		Data: data,
	}
}

// tailLines 读文件最后 n 行，最多读 limit 字节。
//
// 返回的 truncated 表示「文件比 limit 大，前面还有」——不说这件事的话，用户会
// 以为日志只有这么长，然后对着一段没有开头的输出找原因。
func tailLines(path string, n, limit int) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := st.Size()
	read := size
	truncated := false
	if read > int64(limit) {
		read = int64(limit)
		truncated = true
	}
	buf := make([]byte, read)
	if _, err := f.ReadAt(buf, size-read); err != nil && err != io.EOF {
		return nil, false, err
	}
	// 从中间开始读的第一个「行」多半是半截的（被 limit 切掉了），丢掉它。
	text := string(buf)
	if truncated {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, truncated, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
		truncated = true
	}
	for i, line := range lines {
		lines[i] = redactLine(line)
	}
	return lines, truncated, nil
}

// secretInLine 是「这一行里有凭据」的兜底判据。
//
// 日志本来不该出现凭据，但它是**别人写进来的**：一个 provider 的 URL 里带了 key、
// 某个模块把整条请求头打了出来、将来某个插件把 body 记了一笔——这些都不是网关能
// 保证不发生的事。而这个视图会把日志发到浏览器（loopback 也是**本机任何进程**
// 都能读，见 config/view.go 里对 secretKeys 的那段）。所以这里做一次兜底：
// 认得出形状的，一律变成 ***。
//
// 宁可误杀（把一段合法的长串打码），也不放过——日志里少几个字符，比 key 出现在
// 浏览器里轻得多。
var secretInLine = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{12,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|token|authorization|bearer)\b\s*[:=]?\s*[A-Za-z0-9_\-.]{12,}`),
}

func redactLine(line string) string {
	for _, re := range secretInLine {
		line = re.ReplaceAllStringFunc(line, func(m string) string {
			// 保留「哪个字段」那一段：全抹掉的话，排查的人连这里本来有东西都不知道。
			if i := strings.IndexAny(m, ":="); i >= 0 {
				return m[:i+1] + "***"
			}
			return "***"
		})
	}
	return line
}

// metricGroups 把计数器按 Group 归拢：id 管排序与去重，label 只管印。
func seriesGroups() seriesData {
	// 快照只取一次：这是个原子替换出来的 map，取两次会拿到两份可能不同的时刻
	// （中间有请求在跑），同一组里的数就对不上了。
	snap := metrics.Default.Snapshot()
	byID := map[string]*seriesGroup{}
	for _, name := range metrics.SortedKeys(snap) {
		id, label := metrics.Group(name)
		g := byID[id]
		if g == nil {
			g = &seriesGroup{ID: id, Label: label}
			byID[id] = g
		}
		g.Counters = append(g.Counters, seriesEntry{Name: name, Value: snap[name], Hint: metrics.Hint(name)})
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data := seriesData{Groups: make([]seriesGroup, 0, len(ids))}
	for _, id := range ids {
		data.Groups = append(data.Groups, *byID[id])
		for _, c := range byID[id].Counters {
			data.Total += c.Value
		}
	}
	return data
}
