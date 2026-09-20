package main

import (
	"flag"
	"fmt"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/check"
)

// cmdRename 把一条译文从**旧措辞**搬到**新措辞**上。
//
// # 为什么需要它
//
// 键 = 英文原文（msgid）这条规矩买到了「错了会出声」：改了措辞，旧译文立刻变成
// 孤儿，`check` 报 error、界面回落英文——**不会有「看起来正常其实已经错了」的译文**。
// 代价是改一句话要重新翻一句。这条命令就是补那个代价的：措辞改了但意思没变时，
// 译文**搬过去**，不花一次 LLM 调用、也不产生一条「机翻未复核」。
//
// 这与 gettext 的 msgmerge / msgcat 是同一件事：源文变了、译文没变，工具负责把
// 译文接过去，而不是让人重翻。区别只在于这里**由人指名道姓**——自动模糊匹配
// （相似度最高的孤儿）会把「改了个词」和「换了句话」混为一谈，而接错一条译文
// 是静默的错，比多敲一次命令贵得多。
//
// 撤销就是反过来再跑一次：`rename <新> <旧>`（旧那条此刻正不在账本里，条件成立）。
//
// # 判据
//
//   - 旧措辞必须**已经是孤儿**（源码里没人再写它了）：否则说明它还在用，搬走等于
//     删掉一条活着的译文。
//   - 新措辞必须在账本里（源码里确实有这句话）；否则是拼错了。
//   - 新措辞若已有译文：默认拒绝（那多半说明你该翻的是别的），`-force` 才覆盖。
func cmdRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	c := commonFlags(fs)
	lang := fs.String("lang", "zh-Hans", "哪门语言的译文")
	force := fs.Bool("force", false, "新措辞已有译文时也覆盖它")
	fs.Parse(args)
	if err := c.resolve(); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("用法：i18n rename [-lang zh-Hans] <旧英文原文> <新英文原文>")
	}
	old, fresh := fs.Arg(0), fs.Arg(1)

	led, err := check.FreshLedger(c.root, c.catalogDir)
	if err != nil {
		return err
	}
	if _, ok := led.Messages[old]; ok {
		return fmt.Errorf("旧措辞 %q 还在源码里用着——搬走它等于删掉一条活着的译文。"+
			"要么先把调用点改成新措辞，要么你其实想跑的是 sync", old)
	}
	if _, ok := led.Messages[fresh]; !ok {
		return fmt.Errorf("新措辞 %q 不在账本里——源码里没有这句话（拼错了？先跑 extract？）", fresh)
	}

	cat, err := loadOne(c.root, c.catalogDir, *lang)
	if err != nil {
		return err
	}
	entry, ok := cat.Messages[old]
	if !ok || entry.Empty() {
		return fmt.Errorf("%s 里没有 %q 的译文——没得搬（要新翻就用 sync）", *lang, old)
	}
	if prev, exists := cat.Messages[fresh]; exists && !prev.Empty() && !*force {
		return fmt.Errorf("新措辞 %q 已经有译文了（%q）——要覆盖加 -force",
			fresh, firstText(prev))
	}

	delete(cat.Messages, old)
	cat.Messages[fresh] = entry
	if err := writeCatalog(c.root, c.catalogDir, cat); err != nil {
		return err
	}
	mark := ""
	if entry.Machine && !entry.Reviewed {
		mark = "（这条本身还是机翻未复核——搬过来也还是）"
	}
	if entry.Machine && entry.Reviewed {
		mark = "（人工复核过的，搬过来仍然算复核过）"
	}
	fmt.Printf("i18n: %s: 译文搬到新措辞上%s\n  旧 %s\n  新 %s\n  译文 %s\n",
		*lang, mark, old, fresh, firstText(entry))
	return nil
}

// firstText 取一条译文用来显示的那一句（复数条目取 other——中文不分单复数）。
func firstText(e i18n.Entry) string {
	switch {
	case e.Text != "":
		return e.Text
	case e.Other != "":
		return e.Other
	default:
		return e.One
	}
}
