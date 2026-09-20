// Package porthub 把「一个端口上挂多个服务」做成一个组件。
//
// 它存在的意义只有一个：让消费者能问出「**这个进程里有没有这张表**」——有就挂上去
// （共用 gateway 那个端口），没有就自己监听一个端口（fallback）。表本身住在
// lib/porthub（叶子机制），真正服务的是 gateway 那个 http.Server：它在 catch-all
// 里先问表、问不到才当数据面转发（见 modules/gateway/forward 的 dispatch）。
//
// 所以「关掉 porthub」不是分派处的特判，而是依赖没接上：消费者 Optional 拿不到
// 端口，于是走各自的 fallback。
package porthub

import (
	"net/http"

	modules "github.com/rzbdz/newgate/component"
	hub "github.com/rzbdz/newgate/lib/porthub"
)

// Service 是消费者看到的那一面：往表里挂一条。查询那半边不给出去——它只属于
// 分派处（gateway），多一个人问就多一个「谁排谁前面」的问题。
type Service interface {
	Mount(prefix, owner string, h http.Handler) (func(), error)
}

// Capability 是「这张挂载表在这个进程里」这件事本身。
var Capability = modules.NewCapability[Service]("porthub")

// New 声明 porthub 组件。
//
// 没有 Requires、不起监听、不打日志、也没有 Stop：Start 在**每一条** `newgate …`
// 命令里都会跑（modules/i18n 在 Start 里打一行日志，结果每条命令都刷屏——那条
// 教训记在它的注释里），所以这里只做「把自己提供出去」这一件事。挂载是进程内
// 注册，不产生 socket、不产生 goroutine，因此在 CLI 进程里被调用也是安全的。
func New() modules.Component {
	return modules.Component{
		Name:     "porthub",
		Type:     "infra",
		Provides: []modules.Provision{modules.Provide(Capability, Service(hub.Default()))},
	}
}
