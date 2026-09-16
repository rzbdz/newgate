package cli

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/internal/ui/style"
)

func TestUsageNeverExceedsLayoutWidth(t *testing.T) {
	for i, line := range strings.Split(usageText(), "\n") {
		if width := style.VisibleWidth(line); width > style.MaxColumns {
			t.Fatalf("usage line %d is %d columns: %q", i+1, width, line)
		}
	}
}
