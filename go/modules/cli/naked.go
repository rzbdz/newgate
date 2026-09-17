package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/rzbdz/newgate/go/modules/claudecode"
	"github.com/rzbdz/newgate/go/modules/cli/style"
	"github.com/rzbdz/newgate/go/modules/config/store"
)

const nakedDefaultTTL = 60 * time.Second

// cmdNaked 管「裸奔」模式：把 Bash 安全分类器短路成直接批准。
//
// 用法：
//
//	newgate naked off         关闭（默认）
//	newgate naked on          开 60 秒自限窗口
//	newgate naked 30s         自定义时长（支持 30s / 2m / 2min / 1h …）
//	newgate naked forever     永久开（每次请求打 [naked] 日志 + status 红色警告）
//
// 为什么不复用 newgate st on/off：st 管插件层整体开关，不带时间语义；naked
// 需要细粒度到期，而且输出必须足够醒目——用户打开了自家安全门。
func cmdNaked(args []string) int {
	sub := arg(args, 1)
	switch sub {
	case "", "off":
		return nakedOff()
	case "forever":
		return nakedForever()
	case "on":
		return nakedOn(nakedDefaultTTL)
	default:
		d, err := parseNakedDuration(sub)
		if err != nil {
			return die(64, fmt.Sprintf(
				"naked: 不认识的时长 %q（支持 on / forever / off / 30s / 2m / 2min / 1h）", sub))
		}
		return nakedOn(d)
	}
}

func nakedOff() int {
	s := store.LoadState()
	delete(s.ModuleConfig, claudecode.NakedConfigKey)
	if err := store.SaveState(s); err != nil {
		return die(70, err.Error())
	}
	fmt.Println(style.Item(style.OK, "裸奔已关闭 —— 分类器恢复正常工作"))
	notifyProxy()
	return 0
}

func nakedOn(ttl time.Duration) int {
	cfg := claudecode.NakedConfig{
		Mode:      "on",
		ExpiresAt: time.Now().Add(ttl),
	}
	if err := saveNakedConfig(cfg); err != nil {
		return die(70, err.Error())
	}
	fmt.Println(style.Item(style.Warn, fmt.Sprintf(
		"裸奔已开启 —— 分类器短路 %s 后自动关闭", prettyDur(int(ttl.Seconds())))))
	fmt.Println(style.Hint("这段时间内所有 Bash 分类器请求直接被批准，不经过任何安全检查"))
	fmt.Println(style.Hint("提前关闭：newgate naked off"))
	notifyProxy()
	return 0
}

func nakedForever() int {
	cfg := claudecode.NakedConfig{Mode: "forever"}
	if err := saveNakedConfig(cfg); err != nil {
		return die(70, err.Error())
	}
	fmt.Println(style.Item(style.Bad, "裸奔永久模式已开启 —— 分类器被完全短路"))
	fmt.Println(style.Item(style.Bad, "每一个被拦截的请求都打 [naked] 日志，newgate status 持续显示红色警告"))
	fmt.Println(style.Hint("关闭：newgate naked off"))
	notifyProxy()
	return 0
}

func saveNakedConfig(cfg claudecode.NakedConfig) error {
	raw, err := cfg.Marshal()
	if err != nil {
		return err
	}
	s := store.LoadState()
	if s.ModuleConfig == nil {
		s.ModuleConfig = make(map[string][]byte)
	}
	s.ModuleConfig[claudecode.NakedConfigKey] = raw
	return store.SaveState(s)
}

// parseNakedDuration 把用户输入转成 time.Duration。
// 先尝试标准语法（30s / 2m / 1h），再把 "min"/"mins" 规范化为 "m"。
func parseNakedDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// "2min" → "2m"，"5mins" → "5m"
	norm := s
	switch {
	case strings.HasSuffix(norm, "mins"):
		norm = norm[:len(norm)-3]
	case strings.HasSuffix(norm, "min"):
		norm = norm[:len(norm)-2]
	}
	return time.ParseDuration(norm)
}
