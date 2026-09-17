package forward

import (
	"bytes"
	"log"

	"github.com/rzbdz/newgate/go/modules/breaker"
)

// newTestServer 造一个带独立健康表的测试用 Server。
//
// 健康表从进程级单例改成了注入（2026-09-17）：以前所有测试共用一个
// `health.Default`，一个用例把 binding 摘掉，下一个用例就得先手动清干净，
// 漏一次就变成随机失败。现在每个 Server 自带一份，测试之间天然隔离。
func newTestServer() *Server {
	return &Server{Port: 0, Health: breaker.NewTable()}
}

// newLoggingTestServer 同 newTestServer，但把代理日志收进返回的 buffer——
// 「不静默是硬要求」那一类断言（改写了什么、判据是谁认的、证据存哪了）只能
// 在日志上验，不能只看响应和状态。
func newLoggingTestServer() (*Server, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	s := newTestServer()
	s.Logger = log.New(buf, "", 0)
	return s, buf
}
