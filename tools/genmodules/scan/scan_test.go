package scan

import "testing"

// Ident is the only implementation of the "directory name -> import alias" criterion, used by
// the generators of both repositories. The first two rows are the kernel's own directory names
// (underscores, already legal identifiers), the last two are hyphenated directory names from
// the distribution repository -- unmangled, they generate `ext_simple-cli` , a syntax error.
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
