package cli

import (
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
	"github.com/rzbdz/newgate/go/internal/runtime/injection"
	"os"
)

// cmdShim manages the PATH shim for an agent.
//
// Why a PATH shim and not a config file: shells often already export the
// agent's base-URL and auth-token variables, and shell environment outranks
// any config-file env block. Writing the config would be silently overridden
// — the worst failure mode, because everything reports success. A shim sets
// the environment explicitly in the child and wins over anything.
func cmdShim(sub, which string) int {
	switch sub {
	case "install":
		link, err := injection.Install(which)
		if err != nil {
			return die(65, err.Error())
		}
		fmt.Printf("✓ shim %s → newgate\n", link)

		var touched []string
		for _, rc := range injection.RCFiles() {
			changed, err := injection.AddToRC(rc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ %s: %v\n", rc, err)
				continue
			}
			if changed {
				touched = append(touched, rc)
				fmt.Printf("✓ %s prepended to PATH (written into %s)\n", injection.Dir(), rc)
			} else {
				fmt.Printf("- %s already has the PATH line\n", rc)
			}
		}
		if len(touched) > 0 {
			fmt.Printf("  originals backed up under %s/rc/\n", paths.BackupDir())
		}
		if a, ok := agents.Get(which); ok {
			if real, err := a.FindReal(injection.Dir()); err == nil {
				fmt.Printf("\nreal %s: %s\n", which, real)
			} else {
				fmt.Printf("\n⚠ cannot find the real %s: %v\n", which, err)
			}
		}
		if !injection.InPath() {
			fmt.Printf("\n\033[1mnext: reload your shell so PATH takes effect\033[0m\n")
			fmt.Printf("  exec $SHELL -l     # or source ~/.zshrc\n")
			fmt.Printf("then `which %s` should point at %s/%s\n", which, injection.Dir(), which)
		}
		warnShellEnvConflict(which)
		return 0

	case "off":
		if err := injection.Uninstall(which); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		}
		fmt.Printf("✓ shim removed; %s is direct again (the PATH line stays, an empty dir is harmless)\n", which)
		fmt.Println("  new shells take effect immediately; current shell may cache the path — `hash -r` / `rehash`")
		return 0

	case "on":
		link, err := injection.Install(which)
		if err != nil {
			return die(65, err.Error())
		}
		fmt.Printf("✓ shim restored %s\n", link)
		return 0

	case "uninstall":
		for _, n := range injection.Installed() {
			_ = injection.Uninstall(n)
			fmt.Printf("✓ removed shim %s\n", n)
		}
		for _, rc := range injection.RCFiles() {
			changed, err := injection.RemoveFromRC(rc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ %s: %v\n", rc, err)
				continue
			}
			if changed {
				fmt.Printf("✓ removed the PATH line from %s\n", rc)
			}
		}
		fmt.Println("\nfully restored. reload your shell.")
		return 0

	default:
		fmt.Printf("shim dir     %s\n", injection.Dir())
		fmt.Printf("on PATH      %v\n", injection.InPath())
		inst := injection.Installed()
		if len(inst) == 0 {
			fmt.Println("installed    (none)")
		} else {
			fmt.Printf("installed    %s\n", strings.Join(inst, ", "))
		}
		for _, rc := range injection.RCFiles() {
			fmt.Printf("  %-40s PATH line: %v\n", rc, injection.HasBlock(rc))
		}
		for _, n := range inst {
			if a, ok := agents.Get(n); ok {
				if real, err := a.FindReal(injection.Dir()); err == nil {
					fmt.Printf("  real %-8s %s\n", n, real)
				}
			}
		}
		fmt.Println("\nusage: newgate shim install|off|on|uninstall|status [agent]")
		return 0
	}
}
