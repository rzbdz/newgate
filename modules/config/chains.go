package config

import (
	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/resolve"
	"github.com/rzbdz/newgate/modules/config/store"
)

// Chains 是 Config 端口上的那**一个读**（见 api.go 里 Config 的注释）。
//
// # 它为什么是机制而不是某个界面的私事
//
// 「这一档此刻解析成什么、谁被跳过、为什么」是 resolve 的结论，而 resolve 住在
// 本模块内部。任何想摊开这件事的人（`/ui` 的聚合主页、将来的 tui、任何想解释
// 「为什么这一发走了那家」的地方）自己 import store + resolve 都能算出来——发行版
// 里今天就有两处在这么干——但那是**抄一份内核的内部形状**：内核一重构就断在另一个
// 仓库里，而那里没有测试会红。所以端口的形状是「给我结论」，不是「给我原料」。
//
// # 它为什么一次给全部键
//
// 看的人看的是**一屏**：一份档位文件此刻把每一档解析成什么。按键分开问的话，
// 十个键就是十次 store.Load——中间有人改了一笔，那一屏上就是**两个时刻**的配置
// 拼在一起（前几档还认旧文件，后几档已经认新文件）。一次快照算完，那一屏就是
// 同一个时刻的事实。
//
// # 它为什么不问健康表
//
// 熔断摘牌与 probe 延迟（`controlplane.Doc` 的 Available/Rank）会随时间变，而且是
// **跑着的那个进程**的状态；本方法读的是**盘上此刻的样子**。两者混在一起的话，
// 同一份配置在「CLI 里看」与「浏览器里看」会给出不同答案，而差异来自「谁恰好连上了
// daemon」——那种不一致比少一点信息糟得多。链上的 Tone 因此只表达「有没有链头」，
// 不表达健康（见 lib/view 的 ChainRow.Tone）。
func (p *port) Chains(profile string, keys ...string) (*Chains, error) {
	snap, err := store.Load()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read the configuration: {err}", nil)
	}

	// 空 = 此刻全局生效的那一份。**不做 per-agent 的推演**：`Active[agent]` 说的是
	// 「某个客户端从哪条链起步」，而这里问的是「这一份文件解析成什么」，两者是不同的
	// 问题（前者是 `newgate tier` 的多链头视图，它自己那套在 commands.go 里）。
	name := profile
	if name == "" {
		name = snap.State.DefaultProfile
	}

	found := false
	for _, pr := range snap.Profiles {
		if pr.Name == name {
			found = true
			break
		}
	}
	if !found {
		// 报错而不是给一份空链：空链的形状是「这份档位什么都没配」，而事实是
		// **没有这份档位**。静默的话调用方会把它画成一张空卡，读的人以为配置丢了。
		// （`store.Load` 会跳过坏掉的 profile 文件，所以这里也覆盖「文件坏了」那一档
		// ——`newgate doctor` 会报出具体哪个文件。）
		return nil, i18n.E("no such profile: {name}", i18n.A{"name": name})
	}

	// 空 keys = 跟着 domain.Roles（heavy → normal → mid → light，外加 vision）。
	//
	// 顺序由**调用方**决定（传进来就要原样还回去）：谁在前谁在后是产品取舍——`newgate
	// status` 与 doctor 都按能力从高到低排，界面照同一条；而传进来的次序是它唯一能
	// 表达这件事的地方。默认那一套只是「没想法时的合理答案」。
	want := keys
	if len(want) == 0 {
		want = domain.Roles
	}

	out := &Chains{Profile: name, Default: name == snap.State.DefaultProfile}
	for _, key := range want {
		// MaxSteps: 0——**不截断**。maxAttempts 是单次请求的执行上限，不是链的
		// membership（2026-09-17 实查：ark 是第 4 站，传 Attempts() 会把它静默裁掉，
		// 于是屏幕上看起来像「normal 档里根本没有 ark」）。截断的事实在命令行那边由
		// 一行提示说出来，不是把链本身剪短。
		steps, skips := resolve.BuildChain(key, snap.Profiles, snap.Providers, resolve.Opts{
			Active:   name,
			MaxSteps: 0,
		})
		out.Keys = append(out.Keys, Chain{Key: key, Steps: steps, Skips: skips})
	}
	return out, nil
}
