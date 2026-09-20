package gateway

import (
	"sort"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
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

// metricsConcepts 是网关这一刻的展示面。
//
// 它被调用的时机是**有人来看界面**，不是装配——计数器快照因此是「现在」的，
// 而不是「这个进程起来那一刻」的（见 lib/view 的包注释）。
func metricsConcepts() ([]view.Concept, error) {
	return []view.Concept{{
		ID: "gateway.metrics", Kind: view.KindSeries, Title: i18n.T("Counters", nil),
		Data: seriesGroups(),
	}}, nil
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
