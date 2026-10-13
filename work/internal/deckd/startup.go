package deckd

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// n055：CLI 启动时的确认（目录信任等）。
//
// agent-deck v1.15 的 session send 先等 CLI「就绪」再打字，但它把 Claude Code 信任框里的「❯ Yes, I trust this folder」
// 也当成输入框（internal/send/send.go 的兜底规则），会把第一条 brief 连同回车打进确认框：这一版 Claude 的信任框默认停在
// 「No, exit」上，回车就退出；停在「Yes」上则等于替 owner 接受了信任。普通 launch 不会预先接受 Claude 的目录信任，
// 新项目目录第一次起 Claude 时一定会遇到。所以送 brief 之前先看画面：
//   - 画面稳定、没有确认 → 照旧送；
//   - 停在确认上 → 不送；主 agent 显示到中间、对 owner 放开输入（子 agent 提示 owner 按 F2 接管），
//     owner 在 CLI 里自己选；确认消失、画面稳定后 deck 再送第一条 brief，只送一次。

// startupMarkers 是 CLI 启动确认的特征文字（Claude Code 2.x 二进制里的原文；Codex 的目录信任上游通常已预先接受）。
var startupMarkers = []struct{ needle, what string }{
	{"quick safety check", "目录信任确认"},
	{"is this a project you created or one you trust", "目录信任确认"},
	{"is this a directory you created or one you trust", "目录信任确认"},
	{"yes, i trust this folder", "目录信任确认"},
	{"do you trust the files in this folder", "目录信任确认"},
	{"yes, i trust these settings", "项目设置确认"},
	{"do you trust the contents of this directory", "目录信任确认"},
	{"allow codex to work in this folder", "目录信任确认"},
	{"do you want to use this api key", "API key 确认"},
	{"enter to confirm", "启动时的选择确认"},
}

// 带光标的编号选项（「❯ 2. No」与另一条编号选项同屏），启动时出现就是在等人选。光标停在哪一项都算（自查：API key 确认默认在 2 上）；
// 只认 ❯ 与 ›（Claude 回显用户消息用 >，带编号的消息不当成菜单）。
var (
	menuCursor = regexp.MustCompile(`^\s*[❯›]\s*\d+\.\s+\S`)
	menuItem   = regexp.MustCompile(`^\s*(?:[❯›]\s*)?\d+\.\s+\S`)
)

// screenTail 是画面末尾的可见部分（最后 40 行非空行）：上游取画面带 2000 行历史，历史里的旧对话或已消失的确认框不算数（自查）。
func screenTail(screen string) []string {
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return lines
}

// StartupDialog 返回画面末尾 CLI 启动确认的说明；没有返回空。
func StartupDialog(screen string) string {
	tail := screenTail(screen)
	low := strings.ToLower(strings.Join(tail, "\n"))
	for _, m := range startupMarkers {
		if strings.Contains(low, m.needle) {
			return m.what
		}
	}
	cursor, items := false, 0
	for _, l := range tail {
		cursor = cursor || menuCursor.MatchString(l)
		if menuItem.MatchString(l) {
			items++
		}
	}
	if cursor && items >= 2 {
		return "启动时的选择确认"
	}
	return ""
}

// ComposerReady：画面末尾是这个 CLI 真正的输入框（n055 评审 1 B3：两次相同的非空画面不等于就绪，欢迎或加载画面停久了，
// 之后才出的确认框会被上游的就绪判断当成输入框，brief 连回车打进去）。按真机画面（Claude Code 2.1.296、Codex 0.x，
// n051/n055 端到端）认：
//   - claude：一行以 ❯ 开头（顶格，后面是空格或不换行空格），上一行是一整条 ─ 横线（输入框的上边）；
//     信任框的「 ❯ No, exit」前面有空格、上面也不是横线；
//   - codex：一行以 › 开头（顶格、后面不是编号），画面末尾有「for shortcuts」；
//   - 其他工具没有可靠的输入框特征，按「画面稳定、没有确认」判断（报告写明）。
func ComposerReady(tool, screen string) bool {
	tail := screenTail(screen)
	switch tool {
	case "claude":
		for i := 1; i < len(tail); i++ {
			l := tail[i]
			if strings.HasPrefix(l, "❯ ") || strings.HasPrefix(l, "❯\u00a0") || l == "❯" {
				if prev := strings.TrimSpace(tail[i-1]); len([]rune(prev)) >= 10 && strings.Trim(prev, "─") == "" {
					// 输入框要是空的：owner 在里面留了半句草稿时不送，免得 brief 接在草稿后面一起提交（attempt 2 自查）。
					// 新会话空输入框里的灰色提示 Try "…" 不算草稿（与上游 internal/send/guard.go IsComposerPlaceholder 同一规则）
					body := strings.TrimSpace(strings.TrimPrefix(l, "❯"))
					return body == "" || strings.HasPrefix(body, `Try "`) && strings.HasSuffix(body, `"`)
				}
			}
		}
		return false
	case "codex":
		shortcuts := false
		for _, l := range tail {
			shortcuts = shortcuts || strings.Contains(l, "for shortcuts")
		}
		for i := len(tail) - 1; i >= 0; i-- { // 从底往上：历史里 owner 发过的消息也以 › 开头，输入框在最下面（attempt 4 自查）
			l := tail[i]
			if strings.HasPrefix(l, "›") && !menuItem.MatchString(l) {
				// 输入框只在为空或正好是占位时算就绪：其余文字都当 owner 没提交的草稿，免得和 brief 一起提交（评审 2 R3）。
				// Codex 0.162 二进制里只有这一个正常占位；「Waiting for startup」是启动中的占位，不算就绪（attempt 2 自查）
				body := strings.TrimSpace(strings.TrimPrefix(l, "›"))
				return shortcuts && (body == "" || body == codexPlaceholder)
			}
		}
		return false
	}
	return true
}

// codexPlaceholder 是 Codex 空输入框里的占位文字（Codex 0.162 二进制核实）。
const codexPlaceholder = "Ask Codex to do anything"

// sendFirstMessage 送第一条消息（brief 或任务）：先落盘「正在送」，重启时据此不补送（n055 评审 2 R2）。
func (s *Server) sendFirstMessage(ctx context.Context, key string, permit lease.Permit, id, text string) error {
	if err := s.store.MarkBriefSending(key); err != nil {
		return fmt.Errorf("记不下「正在送」，没有送：%w", err)
	}
	return s.agents.upstream.SendMessage(ctx, permit, id, text)
}

// startupTiming 是看画面的节奏；测试换成很短的。
type startupTiming struct {
	poll   time.Duration // 两次取画面的间隔
	settle time.Duration // 启动后最多等多久画面稳定（超时按最后一次画面判断）
	await  time.Duration // 停在确认上时最多等 owner 多久
}

var defaultStartupTiming = startupTiming{poll: time.Second, settle: 20 * time.Second, await: 15 * time.Minute}

func (s *Server) startupTiming() startupTiming {
	if s.agents != nil && s.agents.startup.poll > 0 {
		return s.agents.startup
	}
	return defaultStartupTiming
}

// notReady 是 settleScreen 在等不到就绪画面（取不到画面、一直空屏、一直在变或认不出输入框）时给的说明：这时不直接送，
// 走与启动确认同一条后台路径，等到真正的输入框再送（自查与评审 1 B3：退回直送会让之后才出现的确认框挨打字）。
const notReady = "启动（取不到画面或还没出现输入框）"

// settleScreen 等 CLI 启动后就绪：画面连续两次一样、没有启动确认、并且认得出这个 CLI 的输入框（ComposerReady）。
// 返回确认的说明；就绪返回空；等到上限仍没就绪返回 notReady。
func (s *Server) settleScreen(ctx context.Context, id, tool string) string {
	tm := s.startupTiming()
	deadline := time.Now().Add(tm.settle)
	prev := ""
	for {
		screen, err := s.agents.upstream.PaneOutputPlain(ctx, id)
		if err == nil {
			if d := StartupDialog(screen); d != "" {
				return d
			}
			if strings.TrimSpace(screen) != "" && screen == prev && ComposerReady(tool, screen) {
				return ""
			}
			prev = screen
		}
		if time.Now().After(deadline) || sleepCtx(ctx, tm.poll) != nil {
			return notReady
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// holdForOwner：CLI 停在启动确认上。记下、通知 owner；主 agent 显示到中间并放开输入。然后在后台等确认消失再送 brief。
func (s *Server) holdForOwner(x deckstore.Session, key, id, what, brief string, permit lease.Permit) string {
	var note string
	switch {
	case what == notReady:
		note = "CLI 还没就绪（取不到画面或还没出现输入框）：deck 等到输入框、确认没有启动确认后自动送第一条消息，请在中间看一眼"
	default:
		note = dialogNote(x, what, false)
	}
	s.markAwaiting(x, key, id, note, true)
	s.bgWG.Add(1)
	go func() { defer s.bgWG.Done(); s.briefAfterOwner(x, key, id, brief, permit) }()
	return note
}

// dialogNote 是 CLI 停在启动确认上时给 owner 的说明。uncertain 为真时第一条消息送没送到不确定（重启时见到「正在送」），deck 不会补送。
func dialogNote(x deckstore.Session, what string, uncertain bool) string {
	if uncertain {
		if x.Role == "main" {
			return fmt.Sprintf("CLI 停在%s上：请在中间窗格自己选。deckd 重启前第一条 brief 正在送，送没送到不确定，deck 不会补送，选完请看一眼它有没有收到", what)
		}
		return fmt.Sprintf("CLI 停在%s上：请把它显示在中间、按 F2 接管后自己选。deckd 重启前任务正在送，送没送到不确定，deck 不会补送", what)
	}
	if x.Role == "main" {
		return fmt.Sprintf("CLI 停在%s上：请在中间窗格自己选，选完 deck 会自动送第一条 brief", what)
	}
	return fmt.Sprintf("CLI 停在%s上：请把它显示在中间、按 F2 接管后自己选，交还后 deck 会自动送任务", what)
}

// markAwaiting 记下「等 owner 处理」、通知。open 为真（owner 刚起的会话）时，主 agent 在 owner 没接管别的会话时换进中间；
// 后台等待里补记时 open 为假：不切中间（owner 可能正在别的会话里打字，attempt 4 自查），只在它已经显示在中间时放开输入。
func (s *Server) markAwaiting(x deckstore.Session, key, id, note string, open bool) {
	s.store.SetAwaitingOwner(key, note)
	s.store.SetSessionStatus(key, deckstore.StatusRunning, note)
	s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s", x.Title, note), "deckd")
	if !open {
		if x.Role == "main" && s.view != nil {
			s.view.OpenMainInput(id)
		}
		return
	}
	if x.Role == "main" && s.view != nil && len(s.view.State().TakenOver) == 0 {
		// 显示到中间（新主 agent 还不在打开集合里，用 Open），按最新状态放开输入。owner 正接管着别的会话时不动中间：
		// 切走会把接管交还（自查）；owner 仍可在侧栏点它。失败只影响方便程度，记在通知里
		if err := s.view.Open(id); err != nil {
			s.store.Notify(x.Project, "view", "把主 agent 显示到中间失败（请在侧栏点它）："+deckstore.Clean(err.Error(), 160), "deckd")
		} else if err := s.view.OpenMainInput(id); err != nil {
			s.store.Notify(x.Project, "view", "放开主 agent 输入失败："+deckstore.Clean(err.Error(), 160), "deckd")
		}
	}
}

// briefAfterOwner 在后台等启动确认消失、画面稳定，再送第一条 brief（只送一次）。
// 主 agent 经中间窗格的送达门送（owner 刚打过字就等，送前关输入，送完按最新状态放开）；子 agent 等 owner 交还写入权后送。
func (s *Server) briefAfterOwner(x deckstore.Session, key, id, brief string, permit lease.Permit) {
	s.waitForComposer(x, key, id, false, func(ctx context.Context) bool { return s.sendFirstBrief(ctx, x, key, id, brief, permit) })
}

// uncertainAfterOwner 是重启时见到「正在送」的后台等待（评审 3 B1）：不补送，但在认出空输入框之前一直保持启动期，
// 发话、推送、F8 都挡着，确认框交给 owner；认出后才按「送没送到不确定」收尾。
func (s *Server) uncertainAfterOwner(x deckstore.Session, key, id string) {
	s.waitForComposer(x, key, id, true, func(context.Context) bool { s.finishUncertain(x, key); return true })
}

// finishUncertain 结束「送没送到不确定」的启动期：不补送，主 agent 记为已确认（owner 能在中间直接和它说话，汇报照常转告），
// 子 agent 解除启动期，并请 owner 看一眼。
func (s *Server) finishUncertain(x deckstore.Session, key string) {
	why := "deckd 重启时第一条消息正在送，送没送到不确定：请在中间看一眼，没收到就停掉它重新开"
	if x.Role == "main" {
		s.store.ConfirmBrief(key)
	} else {
		s.store.FinishStart(key)
	}
	s.store.SetSessionStatus(key, deckstore.StatusRunning, why)
	s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s", x.Title, why), "deckd")
}

// waitForComposer 是启动期的后台等待：等启动确认消失、认出这个 CLI 的空输入框、画面稳定，再调 ready（返回 true 表示结束）。
// 期间会话一直在启动期（往它写入的路径都挡着）；CLI 退出就记为已停止；deckd 关闭就停下，状态留给重启时接着处理。
func (s *Server) waitForComposer(x deckstore.Session, key, id string, uncertain bool, ready func(context.Context) bool) {
	ctx := s.background()
	tm := s.startupTiming()
	deadline := time.Now().Add(tm.await)
	poll, told := tm.poll, false
	prev := ""
	for {
		if sleepCtx(ctx, poll) != nil {
			return
		}
		cur, ok := s.store.Session(key)
		if !ok || !cur.Starting || cur.Status != deckstore.StatusRunning {
			return // 已有结果、会话已结束或被停掉：不再送
		}
		if alive := s.agents.bypass.Alive; alive != nil && cur.TmuxSession != "" {
			// CLI 已经退出（例如 owner 在信任框里选了 No, exit）：不再等，记为已停止（自查：原先会一直轮询下去）
			if up, err := alive(ctx, cur.TmuxSession); err == nil && !up {
				why := "CLI 已经退出（可能在启动确认上选了退出），没有送第一条消息"
				if uncertain {
					why = "CLI 已经退出（可能在启动确认上选了退出）；deckd 重启前第一条消息正在送，送没送到不确定"
				}
				s.store.FinishStart(key)
				s.store.SetSessionStatus(key, deckstore.StatusStopped, why)
				s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s", x.Title, why), "deckd")
				return
			}
		}
		if !told && time.Now().After(deadline) {
			// 等久了：说明一次，之后放慢继续等（attempt 2 自查：原先到点就放弃，启动期标记永远不清、owner 之后处理了也不送）
			why := fmt.Sprintf("等了 %d 分钟，CLI 仍停在启动确认上（或一直没出现输入框），还没送第一条 brief；处理完 deck 仍会自动送，不想要了就停掉它", int(tm.await.Minutes()))
			if uncertain {
				why = fmt.Sprintf("等了 %d 分钟，CLI 仍停在启动确认上（或输入框里还有文字、一直没出现空输入框）；deckd 重启后 deck 没再往它写，重启前的第一条消息送没送到不确定。请在中间处理，不想要了就停掉它", int(tm.await.Minutes()))
				if x.Role != "main" {
					why = fmt.Sprintf("等了 %d 分钟，CLI 仍停在启动确认上（或输入框里还有文字、一直没出现空输入框）；deckd 重启后 deck 没再往它写，重启前的任务送没送到不确定。请显示在中间、按 F2 接管处理，不想要了就停掉它", int(tm.await.Minutes()))
				}
			}
			s.store.SetSessionStatus(key, deckstore.StatusRunning, why)
			s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s", x.Title, why), "deckd")
			told, poll = true, 10*tm.poll
		}
		screen, err := s.agents.upstream.PaneOutputPlain(ctx, id)
		if err == nil {
			if d := StartupDialog(screen); d != "" {
				if note := dialogNote(x, d, uncertain); cur.AwaitingOwner != note {
					s.markAwaiting(x, key, id, note, false) // 之前没认出（例如重启前还在看画面）：补记等 owner、放开输入（评审 2 R1）
				}
				prev = ""
				continue
			}
		}
		if err != nil || strings.TrimSpace(screen) == "" {
			prev = ""
			continue
		}
		if screen != prev || !ComposerReady(x.Tool, screen) { // 画面还在变，或还没出现输入框：接着等
			prev = screen
			continue
		}
		if ready(ctx) {
			return
		}
		prev = ""
	}
}

// sendFirstBrief 送一次第一条 brief；返回 true 表示这件事结束了（送达、送达未确认或不该再试）。
func (s *Server) sendFirstBrief(ctx context.Context, x deckstore.Session, key, id, brief string, permit lease.Permit) bool {
	called := false
	send := func() error { called = true; return s.sendFirstMessage(ctx, key, permit, id, brief) }
	var err error
	if x.Role == "main" {
		if s.view != nil {
			err = s.view.DeliverToMain(id, func() error {
				err := send()
				if err == nil || agentdeck.IsDeliveryUnconfirmed(err) {
					s.store.ConfirmBrief(key) // 送达门放开输入前就记下，输入按最新状态放开
				}
				return err
			})
		} else if err = send(); err == nil || agentdeck.IsDeliveryUnconfirmed(err) {
			s.store.ConfirmBrief(key)
		}
		if !called {
			// 送达门在真正发送之前就挡下（owner 刚打过字、中间 client 正被替换、读不到输入时间等）：什么都没送，下一轮再试
			// （attempt 4 自查：原先除了 owner 打字，其余都当发送失败、首条 brief 永久丢掉）
			return false
		}
	} else {
		// 子 agent：owner 接管着就等交还（自动许可拿不到）
		p, perr := s.agents.leases.Automatic(id)
		if perr != nil {
			return false
		}
		err = s.sendFirstMessage(ctx, key, p, id, brief)
	}
	if err != nil && !agentdeck.IsDeliveryUnconfirmed(err) && ctx.Err() != nil {
		// deckd 正在关闭（例如 owner 关了布局），发送被打断：送没送到不确定，不记失败，保留启动期与「正在送」，
		// 重启时按「不确定」处理（attempt 4 自查：原先记成失败，绕过了评审 2 R2 的处理）
		return true
	}
	if x.Role != "main" {
		s.store.FinishStart(key) // 有结果了（送达、未确认或失败）：解除启动期的写入限制
	}
	switch {
	case err == nil:
		s.store.SetSessionStatus(key, deckstore.StatusRunning, "")
		s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」的启动确认已处理，第一条 brief 已送达", x.Title), "deckd")
	case agentdeck.IsDeliveryUnconfirmed(err):
		s.store.SetSessionStatus(key, deckstore.StatusRunning, "brief 已送出，上游没证实提交，请在中间窗格看一眼")
		s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」的第一条 brief 已送出，但上游没证实提交，请在中间窗格看一眼", x.Title), "deckd")
	default:
		why := "启动确认处理后送第一条 brief 失败：" + deckstore.Clean(err.Error(), 200)
		s.store.FinishStart(key)
		s.store.SetSessionStatus(key, deckstore.StatusRunning, why)
		s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s", x.Title, why), "deckd")
	}
	return true
}

// ResumeAwaiting 在 deckd 启动时接着处理重启前停在启动确认上的会话（自查：后台等待随进程消失，提示却还写着会自动送）。
// 主 agent 的 brief 按项目设置重新生成、接着等；子 agent 的任务正文没有落盘，送不了，改说明请 owner 停掉重派。
func (s *Server) ResumeAwaiting() {
	if s.agents == nil {
		return
	}
	set := s.store.Settings()
	for _, x := range s.store.Sessions("") {
		if !x.StartPending() || x.BriefConfirmed || x.Status != deckstore.StatusRunning || x.AgentDeckID == "" {
			continue
		}
		if !x.Starting {
			s.store.MarkStarting(x.Key) // attempt 1 落盘的旧状态只有 AwaitingOwner：补上启动期，后台等待才不会第一轮就退出
		}
		if x.BriefSending {
			// 重启时第一条消息正在送：送没送到不确定（评审 2 R2），不补送。但不能马上解除启动期（评审 3 B1）：画面上可能是确认框，
			// 解除后汇报会被自动推进去。先看一眼画面：已经是空输入框就收尾；否则保持启动期（发话、推送、F8 都挡着），
			// 只对 owner 放开中间输入，后台等到空输入框再收尾（输入框里留着半截消息时也等 owner 处理）
			if s.uncertainReadyNow(x) {
				s.finishUncertain(x, x.Key)
				continue
			}
			note := "deckd 重启时第一条消息正在送，送没送到不确定，deck 不会补送；等它出现空输入框之前不往它转告任何东西，请在中间看一眼（输入框里留着半截消息就自己处理）"
			if x.Role != "main" {
				note = "deckd 重启时任务正在送，送没送到不确定，deck 不会补送；等它出现空输入框之前不往它写，请显示在中间、按 F2 接管看一眼"
			}
			s.markAwaiting(x, x.Key, x.AgentDeckID, note, false)
			x := x
			s.bgWG.Add(1)
			go func() { defer s.bgWG.Done(); s.uncertainAfterOwner(x, x.Key, x.AgentDeckID) }()
			continue
		}
		// 没有「正在送」：第一条消息肯定还没送（评审 2 R1：不能当成已确认），保持启动期、接着等
		p, ok := set.Project(x.Project)
		if x.Role != "main" || !ok {
			why := "deckd 重启前它还没收到任务，任务正文没有保存、送不了：请停掉它重新派"
			s.store.SetAwaitingOwner(x.Key, why)
			s.store.SetSessionStatus(x.Key, deckstore.StatusRunning, why)
			s.store.Notify(x.Project, "spawn", fmt.Sprintf("「%s」%s", x.Title, why), "deckd")
			continue
		}
		permit, err := s.agents.leases.Manual(x.AgentDeckID)
		if err != nil {
			continue
		}
		if x.AwaitingOwner == "" {
			// 与正常启动的「还没就绪」一致：记下等待、对 owner 放开输入（不切中间），看到确认框或输入框再往下走（attempt 4 自查）
			s.markAwaiting(x, x.Key, x.AgentDeckID, "deckd 重启时它还在启动、还没出现输入框：deck 等到输入框后自动送第一条 brief，请在中间看一眼", false)
		}
		x := x
		s.bgWG.Add(1)
		go func() { defer s.bgWG.Done(); s.briefAfterOwner(x, x.Key, x.AgentDeckID, MainBrief(p), permit) }()
	}
}

// uncertainReadyNow 在重启时看画面：隔一个取画面间隔看两次，两次一样、没有启动确认、认得出空输入框，才算可以当场收尾
// （与正常路径一样要求画面稳定，attempt 5 自查；看不到画面或不稳定就交给后台等）。
func (s *Server) uncertainReadyNow(x deckstore.Session) bool {
	ctx, cancel := context.WithTimeout(s.background(), 5*time.Second)
	defer cancel()
	prev := ""
	for i := 0; i < 2; i++ {
		if i > 0 && sleepCtx(ctx, s.startupTiming().poll) != nil {
			return false
		}
		screen, err := s.agents.upstream.PaneOutputPlain(ctx, x.AgentDeckID)
		if err != nil || strings.TrimSpace(screen) == "" || StartupDialog(screen) != "" || !ComposerReady(x.Tool, screen) {
			return false
		}
		if i > 0 && screen != prev {
			return false
		}
		prev = screen
	}
	return true
}

// background 是后台工作的 context：deckd 关闭时取消（daemon 用 WithBackground 接上），没接上时不取消。
func (s *Server) background() context.Context {
	if s.bg != nil {
		return s.bg
	}
	return context.Background()
}

// WaitBackground 等后台工作（等 owner 处理启动确认后送 brief）结束；deckd 关闭时在取消 background 之后、释放写入者锁之前调。
func (s *Server) WaitBackground() { s.bgWG.Wait() }

// WithBackground 接上 deckd 的后台 context（关闭时取消，后台等 owner 处理启动确认的工作随之结束）。
func (s *Server) WithBackground(ctx context.Context) { s.bg = ctx }
