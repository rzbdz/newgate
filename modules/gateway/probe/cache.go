package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/gateway/dialect"
	"github.com/rzbdz/newgate/modules/gateway/quirk"
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

// loadCapabilityCache 读回上一次探活学到的方言/quirk 结论。
//
// 读失败（文件坏、权限不对）返回错误但**照常给一份空缓存**：调用方据此决定
// 是「重新探一轮（花 token）」还是「就这样跑」。以前这个错误被 `_ =` 吞掉，
// 症状是「明明探过，每次还是要重探」——而唯一的线索（为什么读不出来）没了。
func loadCapabilityCache() (*capabilityCache, error) {
	c := &capabilityCache{Targets: map[string]capabilityEntry{}}
	raw, err := os.ReadFile(paths.ProbeCacheFile())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // 没探过，很正常
		}
		return c, i18n.Ef(err, "read capability cache {path}: {err}",
			i18n.A{"path": paths.ProbeCacheFile()})
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return c, i18n.Ef(err, "parse capability cache {path}: {err}",
			i18n.A{"path": paths.ProbeCacheFile()})
	}
	if c.Targets == nil {
		c.Targets = map[string]capabilityEntry{}
	}
	return c, nil
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
		quirk.Default.Mark(t.Provider, t.Model, flags)
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
	// 逐位问，不写死某一位：`Flag` 是位掩码，恢复侧（apply）读的是整张掩码，
	// 所以这里漏一位的症状是**那一位永远存不进去**（见 quirk.AllFlags 的说明）。
	var flags quirk.Flag
	for _, f := range quirk.AllFlags() {
		if quirk.Default.Has(t.Provider, t.Model, f) {
			flags |= f
		}
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
//
// 返回错误时**照常装回能装的部分**（坏文件 = 空缓存），只是把「为什么没装上」
// 交出来。调用方是守护进程，它接的是日志出口——不报的话，现象是「重启之后
// 每个上游都要重新撞一次 404 / 400 才学回来」，而原因（缓存读不出来）没人知道。
func LoadCachedCapabilities() error {
	c, err := loadCapabilityCache()
	for raw := range c.Targets {
		parts := splitTarget(raw)
		c.apply(parts)
	}
	return err
}

// splitTarget 是 Target.String() 的逆。切法归 dialect 定义（两边必须一致：
// 同一批键在缓存恢复和报告展示里必须是同一个 (provider, model)）。
func splitTarget(raw string) Target {
	provider, model, ok := dialect.SplitKey(raw)
	if !ok {
		return Target{}
	}
	return Target{Provider: provider, Model: model}
}
