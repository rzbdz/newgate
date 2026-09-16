package main

import (
	"os"

	agentapi "github.com/rzbdz/newgate/go/modules/confighook/api"
	"github.com/rzbdz/newgate/go/modules/runtime/launch"
)

// runWrapper 是 argv0 分发路径：PATH shim 目录里的 `claude` 符号链接指向本
// 二进制，用户敲 `claude` 就落到这里。真正逻辑在 runtime/launch（CLI 的
// `newgate claude` 包装启动也共用它）。
func runWrapper(a *agentapi.Agent, argv []string) {
	os.Exit(launch.Launch(a, argv[1:], launch.Options{}))
}
