package cli

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestLayoutAuditExemptsEveryTUIAlias(t *testing.T) {
	t.Setenv("NEWGATE_LAYOUT_AUDIT", "1")
	for _, args := range [][]string{{"tui"}, {"menuconfig"}} {
		if shouldAuditLayout(args) {
			t.Fatalf("shouldAuditLayout(%q) = true; TUI must retain the real terminal", args)
		}
	}
	if !shouldAuditLayout([]string{"restart"}) {
		t.Fatal("formatted lifecycle output should be audited")
	}
}

func TestAuditStreamDrainsArbitrarilyLongLine(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.CreateTemp(t.TempDir(), "layout-audit-*")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan []widthViolation, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go auditStream(&wg, srcR, dst, "stdout", results)

	line := strings.Repeat("x", 1024*1024+1) + "\n"
	if _, err := srcW.WriteString(line); err != nil {
		t.Fatal(err)
	}
	_ = srcW.Close()
	wg.Wait()
	if _, err := dst.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	_ = dst.Close()

	if string(copied) != line {
		t.Fatalf("copied %d bytes, want %d", len(copied), len(line))
	}
	if violations := <-results; len(violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(violations))
	}
}

func TestOmoSlotCardKeepsIdentifiersWhole(t *testing.T) {
	values := []string{
		"omo-" + strings.Repeat("slot", 12),
		"agent/" + strings.Repeat("worker", 8),
		"provider/" + strings.Repeat("original-model", 4),
		"normal",
		"heavy",
		"provider/" + strings.Repeat("effective-model", 4),
	}
	got := omoSlotCard(values[0], values[1], values[2], values[3], values[4], values[5])
	for _, value := range values {
		if !strings.Contains(got, value) {
			t.Fatalf("omoSlotCard split %q:\n%s", value, got)
		}
	}
}
