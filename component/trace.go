package component

import (
	"sort"
	"strings"
	"sync"
)

// Trace 是**构图期事件**的出口：内核把「扫到几个组件、每个声明了什么、谁排在
// 谁前面、每个起了多久」这些事实报出去，怎么排版、写到哪里由产品层决定。
//
// 为什么放在内核里而不是产品层自己数：这些事实只有 resolve 与生命周期循环
// 知道——声明顺序、被跳过的弱依赖、拓扑序、每个组件的起停耗时。产品层拿不到
// （Manager 只交出结果，不交出过程），于是「为什么这个模块没起」「为什么顺序
// 是这样」就只能靠猜。
//
// 内核不认识日志、不认识文件、不认识环境变量：产品层装一个函数进来即可
// （见 app.TraceTo）。默认 nil = 什么都不报。
type Trace func(format string, args ...any)

var (
	traceMu sync.RWMutex
	traceFn Trace
)

// SetTrace 安装构图期事件出口；传 nil 关掉。
//
// 进程级设置，与 thinkcache.SetErrorHandler / buildinfo.Set 同类：它是「这台
// 进程往哪报诊断」这一件事，不是某个组件的能力。装配**之前**调用才完整
// （装配一开始就会开始报）。
func SetTrace(fn Trace) {
	traceMu.Lock()
	traceFn = fn
	traceMu.Unlock()
}

// tracef 报一行。没装出口时是空操作，所以调用点不需要判空。
func tracef(format string, args ...any) {
	traceMu.RLock()
	fn := traceFn
	traceMu.RUnlock()
	if fn != nil {
		fn(format, args...)
	}
}

// describeComponent 把一条声明渲染成一行事实：
//
//	breaker            type=infra   提供=[breaker]        需要=[~cli]
//
// `~` 前缀是**弱依赖**（Optional，见 Requirement）：装着就依赖，不装就没有这
// 条边。这一行是排查「模块为什么没起」的第一现场——它是静态声明，与运行时
// 结果无关，所以能直接和下面的排序结果对照。
func describeComponent(c Component) string {
	provided := make([]string, 0, len(c.Provides))
	for _, p := range c.Provides {
		provided = append(provided, p.spec.name)
	}
	needed := make([]string, 0, len(c.Requires))
	for _, r := range c.Requires {
		name := r.spec.name
		if r.optional {
			name = "~" + name
		}
		needed = append(needed, name)
	}
	if len(provided) == 0 {
		provided = append(provided, "-")
	}
	if len(needed) == 0 {
		needed = append(needed, "-")
	}
	sort.Strings(provided)
	sort.Strings(needed)
	return pad(c.Name, 20) + " type=" + pad(string(c.Type), 9) +
		" 提供=[" + strings.Join(provided, " ") + "]" +
		" 需要=[" + strings.Join(needed, " ") + "]"
}

// pad 按显示宽度右侧补空格。组件名是 ASCII（目录名），所以这里不必处理
// CJK 双宽——版式那件事归 lib/style，内核不引入它。
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// Tracef 让**组合根**也能往装配日志里写一行（内核自己的 tracef 是包内的）。
//
// 组合根要用它的场合只有一个、但很关键：**入口解析**——「这次进程调用 route 给
// 谁」必须和装配过程落在同一份日志里，否则排查「我敲的明明是 newgate，为什么起的
// 是别的壳」要同时翻两个地方。它不引入新目的地：出口还是 SetTrace 那一个。
func Tracef(format string, args ...any) { tracef(format, args...) }
