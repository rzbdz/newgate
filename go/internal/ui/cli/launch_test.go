package cli

import (
	"strings"
	"testing"
)

func TestSplitLaunch(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		agent      string
		profile    string
		passthrough string
		wantErr    bool
		errHas     string
	}{
		{"bare agent", []string{"claude"}, "claude", "", "", false, ""},
		{"profile after agent, eq form", []string{"claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"profile after agent, space form", []string{"claude", "--profile", "ds"}, "claude", "ds", "", false, ""},
		{"profile before agent", []string{"--profile", "ds", "claude"}, "claude", "ds", "", false, ""},
		{"run explicit", []string{"run", "claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"passthrough flags", []string{"claude", "--resume", "abc"}, "claude", "", "--resume|abc", false, ""},
		{"passthrough after profile", []string{"claude", "--profile", "ds", "-p", "hi"}, "claude", "ds", "-p|hi", false, ""},
		{"preset alias", []string{"--preset", "toy", "claude"}, "claude", "toy", "", false, ""},
		{"unknown flag before agent", []string{"--bogus", "claude"}, "", "", "", true, "未知选项"},
		{"no agent", []string{"--profile", "ds"}, "", "", "", true, "要启动哪个 agent"},
		{"unknown agent", []string{"nope", "--profile=ds"}, "", "", "", true, "不认识的 agent"},
		{"conflicting profile", []string{"claude", "--profile=ds", "--preset=glm"}, "", "", "", true, "不一致"},
		{"profile missing value", []string{"claude", "--profile"}, "", "", "", true, "需要一个 profile 名"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent, profile, passthrough, err := splitLaunch(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，却成功了（agent=%q profile=%q）", agent, profile)
				}
				if c.errHas != "" && !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("报错 %q 应包含 %q", err, c.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错：%v", err)
			}
			if agent != c.agent {
				t.Errorf("agent = %q，应为 %q", agent, c.agent)
			}
			if profile != c.profile {
				t.Errorf("profile = %q，应为 %q", profile, c.profile)
			}
			got := strings.Join(passthrough, "|")
			if got != c.passthrough {
				t.Errorf("passthrough = %q，应为 %q", got, c.passthrough)
			}
		})
	}
}

func TestDetectLaunch(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"claude"}, true},
		{[]string{"opencode"}, true},
		{[]string{"run"}, true},
		{[]string{"run", "claude"}, true},
		{[]string{"--profile", "ds", "claude"}, true},
		{[]string{"--profile=ds"}, true},
		{[]string{"--preset=toy"}, true},
		{[]string{"status"}, false},
		{[]string{"start"}, false},
		{[]string{"--help"}, false},
		{[]string{"version"}, false},
		{[]string{"profiles"}, false},
		{[]string{"--set-profile", "cheap"}, false},
	}
	for _, c := range cases {
		_, got := detectLaunch(c.args)
		if got != c.want {
			t.Errorf("detectLaunch(%v) = %v，应为 %v", c.args, got, c.want)
		}
	}
}
