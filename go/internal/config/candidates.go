package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Candidates 一个档位的候选列表。接受三种 JSON 写法，内部统一成 list：
//
//	"heavy": {"provider":"p","model":"m"}      对象（现状，继续支持）
//	"heavy": "p/m"                             字符串简写
//	"heavy": ["p/m1", {"provider":"p2",...}]    列表
//
// 统一之后代码里只有一条路径。
type Candidates []Binding

func (c *Candidates) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		*c = nil
		return nil
	}
	switch b[0] {
	case '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		out := make(Candidates, 0, len(raw))
		for i, r := range raw {
			bd, err := parseBinding(r)
			if err != nil {
				return fmt.Errorf("第 %d 个候选: %w", i+1, err)
			}
			out = append(out, bd)
		}
		*c = out
		return nil
	default:
		bd, err := parseBinding(b)
		if err != nil {
			return err
		}
		*c = Candidates{bd}
		return nil
	}
}

func (c Candidates) MarshalJSON() ([]byte, error) {
	// 单个就写成对象，保持文件可读；多个写成数组
	if len(c) == 1 {
		return json.Marshal(c[0])
	}
	return json.Marshal([]Binding(c))
}

func parseBinding(raw json.RawMessage) (Binding, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return Binding{}, fmt.Errorf("空绑定")
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return Binding{}, err
		}
		return ParseBindingString(str)
	}
	var b Binding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Binding{}, err
	}
	if b.Provider == "" || b.Model == "" {
		return Binding{}, fmt.Errorf("provider 和 model 都不能为空: %s", s)
	}
	return b, nil
}

// ParseBindingString 解析 "provider/model"。model 里可以再含 /（有些模型名带斜杠）。
func ParseBindingString(s string) (Binding, error) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "/")
	if i <= 0 || i == len(s)-1 {
		return Binding{}, fmt.Errorf("绑定要写成 provider/model，得到 %q", s)
	}
	return Binding{Provider: s[:i], Model: s[i+1:]}, nil
}

func (b Binding) String() string { return b.Provider + "/" + b.Model }
