package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rzbdz/newgate/go/internal/gateway/dialect"
	"github.com/rzbdz/newgate/go/internal/gateway/quirk"
	"github.com/rzbdz/newgate/go/internal/platform/paths"
)

type capabilityEntry struct {
	Supports uint32    `json:"supports"`
	Known    uint32    `json:"known"`
	Quirks   uint32    `json:"quirks"`
	Checked  time.Time `json:"checked_at"`
}

type capabilityCache struct {
	mu      sync.Mutex
	Targets map[string]capabilityEntry `json:"targets"`
}

func loadCapabilityCache() *capabilityCache {
	c := &capabilityCache{Targets: map[string]capabilityEntry{}}
	raw, err := os.ReadFile(paths.ProbeCacheFile())
	if err == nil {
		_ = json.Unmarshal(raw, c)
	}
	if c.Targets == nil {
		c.Targets = map[string]capabilityEntry{}
	}
	return c
}

func (c *capabilityCache) apply(t Target) bool {
	c.mu.Lock()
	entry, ok := c.Targets[t.String()]
	c.mu.Unlock()
	if !ok {
		return false
	}
	for _, cap := range []dialect.Cap{
		dialect.CapOpenAI, dialect.CapAnthropic, dialect.CapCountTokens,
	} {
		switch {
		case dialect.Cap(entry.Known)&cap == 0:
		case dialect.Cap(entry.Supports)&cap != 0:
			dialect.Mark(t.Provider, t.Model, cap)
		default:
			dialect.MarkUnsupported(t.Provider, t.Model, cap)
		}
	}
	if flags := quirk.Flag(entry.Quirks); flags != 0 {
		quirk.Mark(t.Provider, t.Model, flags)
	}
	return true
}

func (c *capabilityCache) capture(t Target) {
	var supported, known dialect.Cap
	for _, cap := range []dialect.Cap{
		dialect.CapOpenAI, dialect.CapAnthropic, dialect.CapCountTokens,
	} {
		if ok, seen := dialect.Supports(t.Provider, t.Model, cap); seen {
			known |= cap
			if ok {
				supported |= cap
			}
		}
	}
	var flags quirk.Flag
	if quirk.Has(t.Provider, t.Model, quirk.NoThinkingDisable) {
		flags |= quirk.NoThinkingDisable
	}
	c.mu.Lock()
	c.Targets[t.String()] = capabilityEntry{
		Supports: uint32(supported), Known: uint32(known),
		Quirks: uint32(flags), Checked: time.Now(),
	}
	c.mu.Unlock()
}

func (c *capabilityCache) save() error {
	c.mu.Lock()
	raw, err := json.MarshalIndent(c, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.ProbeCacheFile()), 0o2770); err != nil {
		return err
	}
	tmp := paths.ProbeCacheFile() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o660); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o660); err != nil {
		return err
	}
	return os.Rename(tmp, paths.ProbeCacheFile())
}

// LoadCachedCapabilities restores learned dialect/quirk facts without spending
// tokens. The daemon calls this on startup; probe calls it before deciding
// whether auxiliary checks are needed.
func LoadCachedCapabilities() {
	c := loadCapabilityCache()
	for raw := range c.Targets {
		parts := splitTarget(raw)
		c.apply(parts)
	}
}

func splitTarget(raw string) Target {
	for i := range raw {
		if raw[i] == '/' {
			return Target{Provider: raw[:i], Model: raw[i+1:]}
		}
	}
	return Target{}
}
