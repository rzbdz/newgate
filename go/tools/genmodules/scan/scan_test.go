package scan

import "testing"

// Ident 是「目录名 → import 别名」这条判据的唯一实现，两个仓库的生成器都用它。
// 表里前两行是内核自己的目录名（下划线，本来就是合法标识符），后两行是发行版
// 仓库的连字符目录名——不拧的话生成的是 `ext_simple-cli`，一个语法错误。
func TestIdentManglesDirectoryNames(t *testing.T) {
	cases := []struct {
		prefix, dir string
		want        string
	}{
		{"mod_", "gateway", "mod_gateway"},
		{"mod_", "claudecode_deepseek", "mod_claudecode_deepseek"},
		{"ext_", "simple-cli", "ext_simple_cli"},
		{"ext_", "claudecode-glm", "ext_claudecode_glm"},
	}
	for _, c := range cases {
		if got := Ident(c.prefix, c.dir); got != c.want {
			t.Errorf("Ident(%q, %q) = %q，想要 %q", c.prefix, c.dir, got, c.want)
		}
	}
}
