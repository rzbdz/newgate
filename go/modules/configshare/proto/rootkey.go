package proto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rzbdz/newgate/go/modules/config/paths"
)

// ErrNoRootKey 本机还没有根密钥（还没 config trust 过）。
var ErrNoRootKey = errors.New("本机还没有根密钥")

// GenerateRootKey 生成一把新的根密钥。
//
// 只有宿主会调它（`newgate config secrets init`），一生一次。之后这台机器
// 上的一切都从它派生：加密密钥、鉴权令牌。**它不进任何共享通道**——副本是
// 靠人把 blob 拷过去的（这是个刻意的、唯一的手工步骤，见 docs/13）。
func GenerateRootKey() ([]byte, error) {
	root := make([]byte, RootKeyLen)
	if _, err := rand.Read(root); err != nil {
		return nil, fmt.Errorf("取随机数失败: %w", err)
	}
	return root, nil
}

// RootKeyBlob 是给人拷来拷去的那串文本（hex）。
//
// 为什么单独给一个"blob"概念而不是让用户 cat 文件：这条手工分发路径是这个
// 设计里唯一"每台机器各动一次"的操作，它必须一步都不能错。给出一串可复制的
// 文本、且**落盘的就正是这串文本**，意味着 `cat root.key` 打出来的东西
// 直接就能贴到另一台机器上——中间不需要任何转码，也就没有转录错误的空间。
func RootKeyBlob(root []byte) string { return hex.EncodeToString(root) }

// SaveRootKey 把根密钥落盘（0600，hex 文本，见 RootKeyBlob）。
func SaveRootKey(root []byte) error {
	return writeFileAtomic(paths.ConfigShareRootKeyFile(), []byte(RootKeyBlob(root)+"\n"), 0o600)
}

// LoadRootKey 读回根密钥。
//
// 两种输入都认：**hex 文本**（正常路径，SaveRootKey 写的就是它、用户拷的也是
// 它），或者**32 字节原文**（有人用别的方式塞进来的）。为什么容这一手：这条
// 路径的错误代价极高且症状极差——如果只认 hex，一个用 `head -c 32 /dev/urandom
// > root.key` 造密钥的人会得到"根密钥太短"，然后开始怀疑协议、怀疑网络、
// 怀疑 tailscale，而真正的问题是他的文件格式。宽容地接受两种，把这类故障
// 消灭在源头。
func LoadRootKey() ([]byte, error) {
	raw, err := readFileIfExists(paths.ConfigShareRootKeyFile())
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrNoRootKey
	}
	return ParseRootKey(string(raw))
}

// ParseRootKey 解析用户给的 blob（`config trust <blob>`）。
func ParseRootKey(text string) ([]byte, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil, ErrNoRootKey
	}
	if b, err := hex.DecodeString(s); err == nil {
		if len(b) < RootKeyLen {
			return nil, fmt.Errorf("这串有 %d 字节，至少要 %d——像是被截断了（应是一整串 %d 个十六进制字符）",
				len(b), RootKeyLen, RootKeyLen*2)
		}
		return b, nil
	}
	// 不是 hex：也许是 32 字节原文（文件里直接放随机字节的造法）。
	if len(s) >= RootKeyLen {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("这串既不是十六进制（%d 个字符），也不够 %d 字节原文——拷全了吗",
		len(s), RootKeyLen)
}

// RootKeyFingerprint 是根密钥的短指纹，用来**核对两台机器拷的是不是同一把**。
//
// 为什么需要它：两个最常见的部署事故（blob 拷错、只拷了一半）在这一层只能表现
// 为"解密失败"，而那句话会把用户引向怀疑网络。把指纹打印在 `config status`
// 里，用户只要在两台机器上各看一眼就知道是不是同一把。它是派生值的前 8 位，
// 泄露它推不出根密钥。
func RootKeyFingerprint(root []byte) string {
	encKey, _, err := DeriveKeys(root)
	if err != nil {
		return "?"
	}
	return hex.EncodeToString(encKey)[:8]
}

// RootKeyFromEnv 允许用环境变量给根密钥（自动化/测试用，不占正式路径）。
func RootKeyFromEnv() ([]byte, bool, error) {
	v := os.Getenv("NEWGATE_CONFIGSHARE_ROOT_KEY")
	if v == "" {
		return nil, false, nil
	}
	root, err := ParseRootKey(v)
	if err != nil {
		return nil, true, err
	}
	return root, true, nil
}
