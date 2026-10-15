package deckui

import "fmt"

// 设置界面的文字表（n059）：设置界面（settings.go、settings_agents.go、newproject.go）里给 owner 看的文字都从这里取，
// 代码里不再写中文字面量（TestSettingsTextHasNoStrayChinese 把关）。现在只有中文；n060 在这里加英文与切换。
// 不在表里的：deckd、deckstore 回来的报错（原样显示），以及 deckui 其他界面（侧栏、面板）的文字（n060 一并收）。

// T 取一条界面文字；有参数时按 fmt 格式填。表里没有的键原样返回键名（测试会查出来）。
func T(key string, args ...any) string {
	s, ok := textZH[key]
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

var textZH = map[string]string{
	// 顶部与页签
	"title":        "设置",
	"page.project": "项目",
	"page.agents":  "全局 agent",
	"page.trust":   "授权",

	// 档位（文件、命令行 --tier 与 deckd 接口仍用英文 frontier / balanced / economy）
	"tier.frontier": "最强",
	"tier.balanced": "日常",
	"tier.economy":  "便宜",
	"tier.unset":    "（不设）",

	// 分隔
	"sep.list": "、",
	"sep.semi": "；",

	// 项目页
	"project.label":    "项目",
	"project.agents":   "用哪些 agent",
	"project.tiers":    "三档模型",
	"project.noagents": "全局还没有 agent：Tab 到「全局 agent」页按 a 从本机装了的 CLI 里加入",
	"project.empty":    "还没有项目：按 n 新建（选一个目录）",
	"agent.missing":    "（没装）",
	"main.label":       "主 agent",
	"main.unset":       "（未选）",
	"main.uses":        "起来时用 %s",
	"main.nomodel":     "这一档清单里没有它的模型：存不上",
	"main.none":        "先在某一档清单里放进一个勾选的 agent 的模型，主 agent 才有得选",
	"main.moved":       "%s · %s 这一档清单里没有 %s 的模型了：主 agent 改为 %s",

	// 全局 agent 页
	"agents.empty":    "还没有：按 a 从本机装了的 CLI 里加入",
	"agents.add":      "加入本机的 agent",
	"agents.nomodels": "（还没有模型：⏎ 打开后按 + 加）",

	// 授权页
	"trust.title":   "已记住的 Codex 授权",
	"trust.about":   "这些项目里起 Codex 不再问「是否信任这个目录」",
	"trust.empty":   "没有",
	"trust.confirm": "再按一次 d 撤销 %s 的 %s 授权（下次在这个项目起它会重新问你）",
	"trust.revoke":  "撤销 %s 的 %s 授权",

	// 底部：保存状态与提示
	"save":              "保存",
	"status.saving":     "保存中……",
	"status.saved":      "✓ 已保存：对之后起的会话生效，已在跑的会话不变",
	"status.failed":     "✗ 没存上：%s（s 重试；R 重读会丢掉没存上的改动）",
	"status.savedshort": "✓ 已保存",
	"save.conflict":     "⚠ 设置在别处被改过（另一个设置窗口、deck project add、手改或 deck 重启过）：没存上，按 R 重新读取",
	"action.done":       "%s：完成",
	"action.fail":       "%s 失败：%s",
	"load.fail":         "读不到设置：%s",
	"reload.done":       "已重新读取设置",
	"reload.confirm":    "重新读取会丢掉没存上的改动：再按一次 R 确认",
	"quit.saving":       "正在保存",
	"quit.unsaved":      "有改动还没存上（%s）：再按一次 q 放弃并关闭",
	"scroll.up":         "↑ 上面还有 %d 行",
	"scroll.down":       "↓ 下面还有 %d 行",

	// 随光标变化的一行说明（n059：代替以前固定的大段灰字）
	"help.pick":      "空格 勾选或取消：勾选的 agent 的模型都能点名用",
	"help.main":      "空格 换主 agent：在清单里有它模型的「agent · 档位」之间轮换",
	"help.tier":      "⏎ 多选并排顺序：只说档位派活时按顺序取第一个额度没用尽的（未知照常用）  空格 只留一个并轮换",
	"help.agent":     "⏎ 打开：改模型与档位、移出",
	"help.add":       "⏎ 从本机装了的 CLI 里加入，带一份默认模型",
	"help.trust":     "d 撤销（再按一次确认）",
	"help.noproject": "n 新建项目：选一个目录",

	// 各页能按的键（两个空格分组，窄窗口按组折行）
	"keys.project": "Tab 换页  ↑↓ 选  ←→ 换项目  n 新建项目  R 重读  q 关闭",
	"keys.agents":  "Tab 换页  ↑↓ 选  ⏎ 打开  a 加入  R 重读  q 关闭",
	"keys.trust":   "Tab 换页  ↑↓ 选  d 撤销  R 重读  q 关闭",

	// 加入 agent
	"add.title":   "加入本机的 agent",
	"add.keys":    "↑↓ 选  ⏎ 加入  esc 返回",
	"add.loading": "正在查本机装了哪些 agent CLI……",
	"add.none":    "安排 Anpai 认识的 agent 都已加入了",
	"add.about":   "加入时带一份默认模型与档位，加入后可改。命令与启动额外参数不能在这里改。",
	"add.missing": "%s 没装（PATH 里找不到 %s）：先装好再加",
	"add.taken":   "已有 id 为 %s 的 agent",
	"add.done":    "已加入 %s（带默认模型，可在这里改）；回「项目」页勾选它",
	"tools.fail":  "读不到工具目录：%s",

	// 编辑一个全局 agent
	"agent.title":          "%s（%s）",
	"agent.keys":           "↑↓ 选  ⏎ 换档位  + 加模型  e 改 id  x 删模型  r 移出全局  esc 返回",
	"agent.cmd":            "命令 %s",
	"agent.tool":           "agent-deck 工具 %s",
	"agent.extra":          "启动额外参数（安全白名单，只读；要改请手改 ~/.config/deck/agents.yaml）：%s",
	"agent.nomodels":       "还没有模型：按 + 加",
	"agent.tiers":          "档位：最强、日常、便宜；项目每档的清单只能放同一档的模型。",
	"agent.inuse":          "项目「%s」勾选了 %s：先在这些项目里取消勾选，再移出",
	"agent.remove.confirm": "%s 有手写的启动额外参数，移出后就没了（再加回来也不会恢复，要手改 agents.yaml）：再按一次 r 确认移出",
	"agent.removed":        "已把 %s 移出全局可用清单",
	"model.used":           "项目在用：%s",
	"model.inuse.tier":     "%s 在项目「%s」的档位清单里：先在项目里拿掉它，再改档位",
	"model.inuse.del":      "%s 在项目「%s」的档位清单里：先在项目里拿掉它，再删",
	"model.deleted":        "已删掉 %s",
	"model.renamed":        "%s 改成了 %s",
	"model.added":          "加了 %s（%s，⏎ 换档位）",

	// 输入模型 id
	"input.add":        "加模型",
	"input.rename":     "改 %s 的 id",
	"input.prompt":     "%s：",
	"input.asking":     "正在问 %s 有哪些模型……",
	"input.manual":     "手填模型 id",
	"input.renamenote": "改 id（项目档位清单里的也一并改）",
	"input.listfail":   "列不出模型（%s）：手填 id",
	"input.nolist":     "这个 CLI 没有列模型的命令：手填 id",
	"input.has":        "（已有）",
	"input.addkeys":    "打字过滤，↑↓ 选候选，⏎ 加入（不选候选就用输入的文字），esc 取消",
	"input.renamekeys": "⏎ 确定，esc 取消",
	"input.gone":       "agent 不见了",
	"input.badid":      "模型 id 只能是字母数字和 . _ / : @ + -，不能有空格",
	"input.taken":      "%s 已是 %s 的模型",

	// 项目某一档的清单
	"tierview.title": "%s · %s 档",
	"tierview.keys":  "↑↓ 选  ⏎/空格 放进/拿掉  [ ] 或 K J 调顺序  esc 返回",
	"tierview.empty": "勾选的 agent 里没有「%s」档的模型：先在项目里勾选 agent，或在全局 agent 里把模型标成这一档",
	"tierview.about": "只说档位派活时，按序号取第一个额度没显示用尽的；额度未知照常用；全部用尽就不派并说明。点名某个模型时不看这里：勾选的 agent 的模型都能点名用。",

	// 新建项目
	"form.browse.title":  "新建项目 · 选目录",
	"form.browse.keys":   "↑↓ 选  → 或 ⏎ 进入  ← 上一级  在「用这个目录」上按 ⏎ 选定  esc 取消",
	"form.usethis":       "[用这个目录]",
	"form.nosub":         "（没有子目录）",
	"form.confirm.title": "新建项目 · 确认",
	"form.confirm.keys":  "输入改项目 id  ⏎ 建好  esc 回到选目录",
	"form.dir":           "目录",
	"form.id":            "项目 id",
	"form.agents":        "agent",
	"form.main":          "主 agent",
	"form.noagent":       "（没有可用的 agent）",
	"form.later":         "（agent、三档模型、主 agent 建好后可以在「项目」页里改）",
	"form.cancel":        "没有新建项目",
	"form.cantread":      "读不了 %s：%s",
	"form.badpath":       "这个目录不能用作项目目录：%s",
	"form.taken":         "这个目录已经是项目「%s」了",
	"form.gone":          "目录不在了：%s",
	"form.invalid":       "建不了：%s",
	"form.created":       "已建项目「%s」",
	"form.next":          "已建项目「%s」，回到侧栏选中它按 m 起主 agent",
}
