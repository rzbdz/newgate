package forward

import "github.com/rzbdz/newgate/go/modules/breaker"

// newTestServer 造一个带独立健康表的测试用 Server。
//
// 健康表从进程级单例改成了注入（2026-09-17）：以前所有测试共用一个
// `health.Default`，一个用例把 binding 摘掉，下一个用例就得先手动清干净，
// 漏一次就变成随机失败。现在每个 Server 自带一份，测试之间天然隔离。
func newTestServer() *Server {
	return &Server{Port: 0, Health: breaker.NewTable()}
}
