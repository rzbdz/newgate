package cli

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/store"
	"github.com/rzbdz/newgate/go/internal/ui/tui"
)

// cmdDebug turns on full request logging.
//
// It expires on its own by default: a single request can log 8KB or more
// (opencode's system prompt alone is ~97KB), so leaving it on indefinitely
// fills the disk.
func cmdDebug(args []string) int {
	on := len(args) > 1 && truthy(args[1])
	ttl := 30 * time.Minute
	if on {
		for _, a := range args {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				ttl = time.Duration(n) * time.Minute
			}
		}
		if has(args, "--forever") {
			ttl = 0
		}
	}
	if err := store.SetDebug(on, debugUntil(ttl)); err != nil {
		return die(70, err.Error())
	}
	if !on {
		fmt.Println("✓ debug off")
		return 0
	}
	if ttl > 0 {
		fmt.Printf("✓ debug on, auto-off in %v (guards against filling the disk)\n", ttl)
		fmt.Println("  keep it on indefinitely: newgate debug on --forever")
	} else {
		fmt.Println("✓ debug on, no expiry — remember to run `newgate debug off`")
	}
	fmt.Printf("  log %s, rotating at 16MB keeping 4 files\n", paths.LogFile())
	notifyProxy()
	if daemon.Running() != nil {
		fmt.Println("  effective immediately; no restart needed")
	}
	return 0
}

func debugUntil(ttl time.Duration) string {
	if ttl <= 0 {
		return ""
	}
	return time.Now().Add(ttl).Format(time.RFC3339)
}

func cmdSchemaRepair(on bool) int {
	if err := store.SetSchemaRepair(on); err != nil {
		return die(70, err.Error())
	}
	if on {
		fmt.Println(`✓ schema repair on (adds "required": [] where a tool schema omits it)`)
	} else {
		fmt.Println("✓ schema repair off — tools are forwarded exactly as received")
	}
	notifyProxy()
	return 0
}

func cmdTUI() int {
	if err := tui.Run(); err != nil {
		return die(70, err.Error())
	}
	return 0
}
