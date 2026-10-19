package i18n

// errHandZH / errHandEN 是手写的错误码。批量生成的在 err_gen.go。
// 两边用 init 并进 zh / en。键重复就 panic，免得静默盖掉。
var errHandZH = map[Key]string{
	"err.deckclient.status":  "deckd %d：%s",
	"err.deckclient.timeout": "deckd 在 %s 内没有答复（%w）：操作可能仍在进行，先用 deck agent list 核对，不要直接重试",
	"err.deckclient.dial":    "连不上 deckd（%w）：先运行 deck daemon",

	"err.deckstore.errversion":                    "这个决定已经变了（你看到的是第 %d 版，现在是第 %d 版），请刷新后再答",
	"err.deckstore.errallexhausted":               "项目 %s 的 %s 档清单里能用的模型额度都已用尽（%s）：不派；等额度恢复，或在设置里给这一档加别家的模型",
	"err.deckstore.errmixed.proj":                 "projects.yaml（第 %d 代）",
	"err.deckstore.errmixed.missing":              "projects.yaml（不存在，按第 0 代）",
	"err.deckstore.errmixed.body":                 "%s 下的 agents.yaml（第 %d 代）与 %s来自不同的保存，上一次保存只写成了一半；不用这份新旧混合的配置。恢复：把代数较新的那份换回它的 .prev（上一代；没有 .prev 说明它是第一次写，删掉即回到默认），或核对内容后把两份的 generation 改成一样（缺的那份补上）",
	"err.deckstore.errsettings.hand":              "设置文件在 deckd 之外被改过（手改）：没有保存，免得盖掉；deck 已读入改过的那份，重新读取后再改",
	"err.deckstore.errsettings.else":              "设置在你打开之后被别处改过（另一个设置窗口、deck project add，或 deck 重启过）：没有保存；重新读取后再改",
	"err.deckstore.errbroken":                     "deck 的存储写失败过（%v），已停止接受写入与派工；修好后重启 deckd",
	"err.deckstore.erroverenvelope":               "项目 %s 超出预算包络 %s v%d：%s（当前子 agent %d/%d，frontier %d/%d）",
	"err.agentdeck.errsubcommand":                 "拒绝执行上游子命令 %q：不在 agentdeck 包的允许清单内（%s）",
	"err.agentdeck.errdelivery.reached":           "消息到了会话 %s 的画面，但上游没确认提交（%s）",
	"err.agentdeck.errdelivery.missed":            "上游没证实消息到了会话 %s、也没证实提交（%s）",
	"err.agentdeck.errambiguous":                  "会话 %s 同时存在于 %d 个 tmux server 上（%v），说不清该连哪一个，拒绝直连",
	"err.deckd.startup.trust":                     "目录信任确认",
	"err.deckd.startup.settings":                  "项目设置确认",
	"err.deckd.startup.apikey":                    "API key 确认",
	"err.deckd.startup.choice":                    "启动时的选择确认",
	"err.deckd.startup.notready":                  "启动（取不到画面或还没出现输入框）",
	"err.deckd.startup.main":                      "CLI 停在%s上：请在中间窗格自己选，选完 deck 会自动送第一条 brief",
	"err.deckd.startup.sub":                       "CLI 停在%s上：请把它显示在中间、按 F2 接管后自己选，交还后 deck 会自动送任务",
	"err.deckd.startup.mainunc":                   "CLI 停在%s上：请在中间窗格自己选。deckd 重启前第一条 brief 正在送，送没送到不确定，deck 不会补送，选完请看一眼它有没有收到",
	"err.deckd.startup.subunc":                    "CLI 停在%s上：请把它显示在中间、按 F2 接管后自己选。deckd 重启前任务正在送，送没送到不确定，deck 不会补送",
	"err.deckd.startup.banner":                    "「%s」%s",
	"err.deckd.launchoutcome.nosession":           "上游没回会话号",
	"err.deckd.serverrunspawn.projectctl":         "projectctl start：%w",
	"err.agentdeck.checkupstreamconfignobypass.3": "%s：%w",
	"err.agentdeck.runmux.1":                      "%w：%s",
	"err.agentdeck.sendresult.delivery":           "（delivery=%s）",
	"err.agentdeck.nomatcherror.part":             "%s（%s）",
}

var errHandEN = map[Key]string{
	"err.deckclient.status":  "deckd %d: %s",
	"err.deckclient.timeout": "deckd did not answer within %s (%w): the operation may still be running; check with deck agent list before retrying",
	"err.deckclient.dial":    "cannot reach deckd (%w): run deck daemon first",

	"err.deckstore.errversion":                    "this decision changed (you saw version %d, it is now version %d); refresh and answer again",
	"err.deckstore.errallexhausted":               "project %s tier %s has no usable model left with quota (%s): not dispatching; wait for quota, or add another provider's model for this tier in settings",
	"err.deckstore.errmixed.proj":                 "projects.yaml (generation %d)",
	"err.deckstore.errmixed.missing":              "projects.yaml (missing, treated as generation 0)",
	"err.deckstore.errmixed.body":                 "%s has agents.yaml (generation %d) and %s from different saves; the last save only finished halfway. This mixed config is not used. Recovery: put the newer file back from its .prev (the previous generation; if there is no .prev it was the first write, delete it to return to the default), or check the contents and set both generation values the same (add the missing file)",
	"err.deckstore.errsettings.hand":              "settings files were changed outside deckd (edited by hand): not saved, so the edits are not overwritten; deck has loaded the edited copy, read it again before changing it",
	"err.deckstore.errsettings.else":              "settings changed elsewhere after you opened them (another settings window, deck project add, or deck restarted): not saved; read them again before changing them",
	"err.deckstore.errbroken":                     "deck storage failed a write (%v) and has stopped accepting writes and dispatch; fix it and restart deckd",
	"err.deckstore.erroverenvelope":               "project %s is over budget envelope %s v%d: %s (sub-agents %d/%d, frontier %d/%d)",
	"err.agentdeck.errsubcommand":                 "refused upstream subcommand %q: not on the agentdeck allowlist (%s)",
	"err.agentdeck.errdelivery.reached":           "the message reached the screen of session %s, but upstream did not confirm submit (%s)",
	"err.agentdeck.errdelivery.missed":            "upstream did not confirm the message reached session %s, and did not confirm submit (%s)",
	"err.agentdeck.errambiguous":                  "session %s is on %d tmux servers (%v); which one to attach is unclear, so direct attach is refused",
	"err.deckd.startup.trust":                     "directory trust prompt",
	"err.deckd.startup.settings":                  "project settings prompt",
	"err.deckd.startup.apikey":                    "API key prompt",
	"err.deckd.startup.choice":                    "startup choice prompt",
	"err.deckd.startup.notready":                  "startup (no screen yet, or the input box has not appeared)",
	"err.deckd.startup.main":                      "CLI is stopped on %s: choose it yourself in the center pane; deck sends the first brief after you choose",
	"err.deckd.startup.sub":                       "CLI is stopped on %s: show it in the center and press F2 to take over, then choose it yourself; deck sends the task after you hand it back",
	"err.deckd.startup.mainunc":                   "CLI is stopped on %s: choose it yourself in the center pane. The first brief was being sent when deckd restarted, so delivery is uncertain and deck will not send it again; after you choose, check whether it arrived",
	"err.deckd.startup.subunc":                    "CLI is stopped on %s: show it in the center and press F2 to take over, then choose it yourself. The task was being sent when deckd restarted, so delivery is uncertain and deck will not send it again",
	"err.deckd.startup.banner":                    "\"%s\" %s",
	"err.deckd.launchoutcome.nosession":           "upstream did not return a session id",
	"err.deckd.serverrunspawn.projectctl":         "projectctl start: %w",
	"err.agentdeck.checkupstreamconfignobypass.3": "%s: %w",
	"err.agentdeck.runmux.1":                      "%w: %s",
	"err.agentdeck.sendresult.delivery":           " (delivery=%s)",
	"err.agentdeck.nomatcherror.part":             "%s (%s)",
}

func init() {
	mergeErr(zh, errHandZH, "zh")
	mergeErr(en, errHandEN, "en")
}

func mergeErr(dst, src map[Key]string, name string) {
	for k, v := range src {
		if _, ok := dst[k]; ok {
			panic("i18n: duplicate " + name + " key " + string(k))
		}
		dst[k] = v
	}
}
