package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/go/internal/agents"
	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
	"github.com/rzbdz/newgate/go/internal/gateway/health"
	"github.com/rzbdz/newgate/go/internal/runtime/daemon"
	"github.com/rzbdz/newgate/go/internal/store"
)

// cmdSetProfile switches the chain head. An empty agent sets the global
// default; naming an agent switches only that agent, leaving every other
// agent — including ones with sessions in flight — untouched.
func cmdSetProfile(agent, name string) int {
	if err := store.SetActiveProfile(agent, name); err != nil {
		return die(65, err.Error())
	}
	scope := "global default"
	if agent != "" {
		scope = "agent " + agent
	}
	fmt.Printf("✓ %s → profile %s\n", scope, name)

	if pr, err := store.LoadProfile(name); err == nil {
		if pr.Description != "" {
			fmt.Printf("  %s\n", pr.Description)
		}
		if pr.Pinned {
			fmt.Printf("  pinned: the chain stops here; a failure is reported rather than substituted\n")
		}
		for _, tier := range domain.Roles {
			if b, ok := pr.Resolve(tier); ok {
				fmt.Printf("    %-8s %s\n", tier, b)
			}
		}
	}
	notifyProxy()
	if daemon.Running() != nil {
		fmt.Println("\nEffective immediately. Sessions already running are unaffected.")
	} else {
		fmt.Println("\nNote: the proxy is not running. Start it with `newgate start`.")
	}
	return 0
}

func cmdProfiles() int {
	names, err := store.ListProfiles()
	if err != nil {
		return die(65, "cannot read mappings: "+err.Error())
	}
	st := store.LoadState()
	type row struct{ p *domain.Profile }
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := store.LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Prio() != ps[j].Prio() {
			return ps[i].Prio() < ps[j].Prio()
		}
		return ps[i].Name < ps[j].Name
	})

	fmt.Printf("%-4s %-14s %-22s %s\n", "PRIO", "PROFILE", "FLAGS", "DESCRIPTION")
	fmt.Println(strings.Repeat("─", 88))
	for _, p := range ps {
		var flags []string
		if p.Pinned {
			flags = append(flags, "pinned")
		}
		if p.Excluded {
			flags = append(flags, "excluded")
		}
		if p.Name == st.DefaultProfile {
			flags = append(flags, "default")
		}
		for agent, ap := range st.Active {
			if ap == p.Name {
				flags = append(flags, "←"+agent)
			}
		}
		fmt.Printf("%4d %-14s %-22s %s\n", p.Prio(), p.Name,
			strings.Join(flags, ","), p.Description)
	}

	if len(st.Active) > 0 {
		fmt.Println("\nPer-agent overrides:")
		for agent, p := range st.Active {
			fmt.Printf("  %-12s %s\n", agent, p)
		}
	}
	fmt.Printf("\nGlobal default: %s\n", st.DefaultProfile)
	return 0
}

// cmdTier prints the fallback chain for a tier, including why each candidate
// was skipped. This view is the interface: if it cannot answer "why this one,
// and what do I change" on one screen, the configuration is unusable.
func cmdTier(which string) int {
	snap, err := store.Load()
	if err != nil {
		return die(65, err.Error())
	}
	st := snap.State

	tiers := domain.Roles
	if which != "" {
		tiers = []string{which}
	}

	// Show the chain for every distinct chain head in play, so per-agent
	// differences are visible rather than hidden behind one global view.
	heads := map[string][]string{st.DefaultProfile: {"(default)"}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	var headNames []string
	for h := range heads {
		headNames = append(headNames, h)
	}
	sort.Strings(headNames)

	for _, head := range headNames {
		sort.Strings(heads[head])
		fmt.Printf("\n\033[1mchain head %s\033[0m  (used by: %s)   maxAttempts=%d budget=%dms\n",
			head, strings.Join(heads[head], ", "),
			st.Chain.Attempts(), st.Chain.Budget())

		for _, tier := range tiers {
			steps, skips := resolve.BuildChain(tier, snap.Profiles, snap.Providers,
				resolve.Opts{
					Active:    head,
					Available: health.Default.Available,
					MaxSteps:  st.Chain.Attempts(),
				})
			fmt.Printf("\n  %-8s", tier)
			if len(steps) > 0 {
				fmt.Printf(" → \033[36m%s\033[0m\n", steps[0].Binding)
			} else {
				fmt.Printf(" → \033[31mno usable candidate\033[0m\n")
			}
			for i, s := range steps {
				mark := "   "
				if i == 0 {
					mark = " → "
				}
				fmt.Printf("   %s%d. %-14s %-40s\n", mark, i+1, s.Profile, s.Binding)
			}
			for _, sk := range skips {
				t := sk.Target
				if t == "" {
					t = "(whole profile)"
				}
				fmt.Printf("     -  %-14s %-40s \033[2m%s\033[0m\n", sk.Profile, t, sk.Reason)
			}
		}
	}
	return 0
}

// cmdAgents lists known agents and their slots.
func cmdAgents() int {
	st := store.LoadState()
	names := agents.Names()
	sort.Strings(names)
	for _, n := range names {
		a, _ := agents.Get(n)
		fmt.Printf("\n\033[1m%s\033[0m  dialect=%s  profile=%s\n",
			a.ID, a.Dialect, st.ActiveFor(a.ID))
		if a.Notes != "" {
			fmt.Printf("  %s\n", a.Notes)
		}
		if len(a.Slots) == 0 {
			fmt.Printf("  slots: discovered from config files at bootstrap\n")
			continue
		}
		fmt.Printf("  %-12s %-8s %-34s %s\n", "SLOT", "TIER", "ENV VAR", "NOTE")
		for _, s := range a.Slots {
			fmt.Printf("  %-12s %-8s %-34s %s\n", s.Name, s.Tier, s.EnvVar, s.Desc)
		}
	}
	fmt.Printf("\nSwitch one agent only:  newgate --set-profile <name> --agent <agent>\n")
	return 0
}
