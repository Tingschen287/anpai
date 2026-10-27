package deckui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// API 是侧栏与标签条对 deckd 的全部调用（测试可换）。
type API interface {
	Fetch(ctx context.Context) (Data, error)
	View(ctx context.Context, op, session string) error
	// StartMain 为项目起主 agent（n055：侧栏按 m，与 deck agent start-main 走同一个接口、同一套准入）
	StartMain(ctx context.Context, project string) (StartMainResult, error)
}

// StartMainResult 是 POST /v1/projects/{id}/main 的结果：开成（201）或等 owner 批准（202 pending）。
type StartMainResult struct {
	Session        deckstore.Session `json:"session"`
	BriefDelivered bool              `json:"brief_delivered"`
	Note           string            `json:"note"`
	NoteCode       string            `json:"note_code,omitempty"`
	NoteArgs       []any             `json:"note_args,omitempty"`
	Pending        string            `json:"pending"`
	Next           string            `json:"next"`
}

// DeckAPI 走 deckd 的 unix socket。
type DeckAPI struct{ C *deckclient.Client }

// Fetch 取项目、会话（带运行状态）与中间窗格状态。
func (a DeckAPI) Fetch(ctx context.Context) (Data, error) {
	var d Data
	if err := a.C.Get(ctx, "/v1/projects", &d.Projects); err != nil {
		return d, err
	}
	if err := a.C.Get(ctx, "/v1/sessions", &d.Sessions); err != nil {
		return d, err
	}
	var v viewstate.State
	if err := a.C.Get(ctx, "/v1/view", &v); err != nil {
		var de *deckclient.Error
		if !errors.As(err, &de) || de.Status != 503 { // 503：布局还没接上，状态为空
			return d, err
		}
	}
	d.View = v
	return d, nil
}

// View 对中间窗格做一个动作（open、focus、hide、takeover）。
func (a DeckAPI) View(ctx context.Context, op, session string) error {
	var out any
	return a.C.Do(ctx, "POST", "/v1/view", map[string]string{"op": op, "session": session}, &out)
}

// StartMain 起主 agent。每次都是新的启动请求 id（失败或等批准之后再按一次就是新的请求）。
func (a DeckAPI) StartMain(ctx context.Context, project string) (StartMainResult, error) {
	var out StartMainResult
	b := make([]byte, 8)
	rand.Read(b)
	err := a.C.WithTimeout(deckclient.LongTimeout).Do(ctx, "POST", "/v1/projects/"+url.PathEscape(project)+"/main",
		map[string]string{"request_id": "req-" + hex.EncodeToString(b)}, &out)
	return out, err
}

// startMainNote 是起主 agent 的结果说明（侧栏底部显示，最长留 12 秒）；ok 为 false 时按出错显示。
// 返回的是 lazyText：每次画的时候才按当前语言取文字，错误也留着原错误（返工 2，M1：以前这里直接存译文，
// 12 秒内 owner 切了语言，这条还是旧语言）。
func startMainNote(project string, r StartMainResult, err error) (text lazyText, ok bool) {
	switch {
	case err != nil:
		return later(func() string { return T("main.start.fail", project, errText(err)) }), false
	case r.Pending != "":
		pending := r.Pending
		return later(func() string { return T("main.start.pending", project, pending) }), true
	case r.Note != "":
		code, args, raw := r.NoteCode, r.NoteArgs, r.Note
		return later(func() string {
			note := raw
			if code != "" {
				note = deckclient.Text(code, args, raw)
			}
			return oneLine(deckstore.Display(T("main.start.note", project, note), 160))
		}), true
	}
	return later(func() string { return T("main.start.ok", project) }), true
}

// startMainText 是 startMainNote 此刻的文字（测试和只要当前文字的调用方用）。
func startMainText(project string, r StartMainResult, err error) (text string, ok bool) {
	n, ok := startMainNote(project, r, err)
	return n.String(), ok
}

// errText 把错误清洗成一行可显示的文字。中文仍是原文。英文按错误码翻译。
func errText(err error) string { return oneLine(deckstore.Display(deckclient.Show(err), 120)) }

// errNoteShort 是「这个错误」的一行提示（120 字上限）：留着原错误，每次画时按当前语言翻译。
func errNoteShort(err error) lazyText { return func() string { return errText(err) } }

type driver struct {
	old    *term.State
	events chan Event
	resize chan os.Signal
}

func openTerm() (*driver, error) {
	old, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return nil, err
	}
	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	d := &driver{old: old, events: make(chan Event, 64), resize: make(chan os.Signal, 1)}
	signal.Notify(d.resize, syscall.SIGWINCH)
	go d.read()
	return d, nil
}

func (d *driver) close() {
	os.Stdout.WriteString("\x1b[?1000l\x1b[?1006l\x1b[?25h\x1b[?1049l")
	term.Restore(os.Stdin.Fd(), d.old)
}

func (d *driver) read() {
	buf := make([]byte, 512)
	var rest []byte
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			close(d.events)
			return
		}
		evs, r := Parse(append(rest, buf[:n]...), false)
		if len(r) == 1 && r[0] == 0x1b { // 单独一个 ESC：没有后续就当 esc 键
			time.Sleep(30 * time.Millisecond)
			e, _ := Parse(r, true)
			evs, r = append(evs, e...), nil
		}
		rest = r
		for _, e := range evs {
			d.events <- e
		}
	}
}

func size() (int, int) {
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 || h <= 0 {
		return 32, 24
	}
	return w, h
}

// cellSize 取字符格的像素尺寸（tmux 从外层终端拿到时会传给窗格），取不到返回 0。
func cellSize() (int, int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}

// selectCenter 把键盘焦点交还中间窗格（点完侧栏或标签后，打字应进 CLI）。
func selectCenter(layoutSock string) {
	panes, err := agentdeck.LayoutPanes(context.Background(), layoutSock)
	if err != nil {
		return
	}
	for _, p := range panes {
		if p.Role == layout.RoleCenter {
			agentdeck.LayoutSelect(context.Background(), layoutSock, p.ID)
		}
	}
}

// loop 是两个界面共用的主循环：定时取数据、处理输入、只在画面变化时重画。
type loop struct {
	api      API
	kick     chan struct{}
	data     chan Data
	errText  lazyText // 出错的提示：存怎么取文字，不存译文（返工 2，M1）
	errUntil time.Time
	info     lazyText // 动作的结果说明（绿色），显示 12 秒
	infoTill time.Time
}

func newLoop(api API) *loop {
	l := &loop{api: api, kick: make(chan struct{}, 1), data: make(chan Data, 1)}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			d, err := api.Fetch(ctx)
			cancel()
			if err != nil {
				d.Err = T("ui.deckd") + errText(err)
			}
			select {
			case l.data <- d:
			default:
				<-l.data
				l.data <- d
			}
			select {
			case <-t.C:
			case <-l.kick:
			}
		}
	}()
	return l
}

func (l *loop) refresh() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// act 做一个动作；出错就在界面上显示 6 秒。
func (l *loop) act(op, session string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // 接管最多等 10 秒
	defer cancel()
	err := l.api.View(ctx, op, session)
	if err != nil {
		l.errText, l.errUntil = errNoteShort(err), time.Now().Add(6*time.Second)
	} else {
		l.errText = nil
	}
	l.refresh()
	return err
}

func (l *loop) merge(d Data) Data {
	if !l.errText.empty() && time.Now().Before(l.errUntil) && d.Err == "" {
		d.Err = l.errText.String()
	}
	if !l.info.empty() && time.Now().Before(l.infoTill) {
		d.Info = l.info.String()
	}
	return d
}

// say 在底部显示一条结果说明：ok 为 false 时按出错显示。
func (l *loop) say(text lazyText, ok bool) {
	if ok {
		l.info, l.infoTill, l.errText = text, time.Now().Add(12*time.Second), nil
	} else {
		l.errText, l.errUntil, l.info = text, time.Now().Add(12*time.Second), nil
	}
}

// startedMain 是后台起主 agent 的结果（交回主循环处理，免得并发改界面状态）。
type startedMain struct {
	project string
	res     StartMainResult
	err     error
}

// RunSidebar 跑左侧会话栏。
// RunSidebar 跑侧栏。modeOf 给出布局当前的 logo 画法（每秒读一次，变了就切换，n056 评审 1 B2）；nil 时按 logo.Mode()。
func RunSidebar(api API, layoutSock string, modeOf func() string) error {
	if modeOf == nil {
		modeOf = logo.Mode
	}
	d, err := openTerm()
	if err != nil {
		return err
	}
	defer d.close()
	sb := &Sidebar{Mode: modeOf()}
	sb.CellW, sb.CellH = cellSize()
	l := newLoop(api)
	var raw Data
	last := ""
	draw := func() {
		sb.Data = l.merge(raw)
		w, h := size()
		if out := sb.Render(w, h).Encode(w, h); out != last {
			os.Stdout.WriteString(out)
			last = out
		}
	}
	open := func(i int) {
		list := sb.Data.Ordered()
		if i < 0 || i >= len(list) || !Openable(list[i]) {
			return
		}
		if l.act("open", list[i].AgentDeckID) == nil {
			selectCenter(layoutSock)
		}
	}
	// 起主 agent 要等上游起 CLI、送 brief（可能一分钟以上）：放到后台，结果经 started 交回；同一个项目在起时不重复发
	started := make(chan startedMain, 4)
	starting := map[string]bool{}
	startMain := func(project string) {
		if project == "" {
			l.say(later(func() string { return T("main.noproject") }), false)
			return
		}
		if starting[project] {
			l.say(later(func() string { return T("main.starting.wait", project) }), true)
			return
		}
		starting[project] = true
		l.say(later(func() string { return T("main.starting", project) }), true)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), deckclient.LongTimeout)
			defer cancel()
			r, err := api.StartMain(ctx, project)
			started <- startedMain{project, r, err}
		}()
	}
	tick := time.NewTicker(time.Second) // 让出错提示按时消失
	defer tick.Stop()
	for {
		select {
		case raw = <-l.data:
		case <-d.resize:
			sb.CellW, sb.CellH = cellSize()
			last = ""
		case <-tick.C:
			if m := modeOf(); m != sb.Mode {
				sb.Mode, sb.cache, last = m, nil, ""
			}
		case r := <-started:
			delete(starting, r.project)
			text, ok := startMainNote(r.project, r.res, r.err)
			l.say(text, ok)
			if r.err == nil && r.res.Pending == "" && r.res.Session.AgentDeckID != "" {
				// 开成：换进中间（停在启动确认上时 deckd 已经换过去，再换一次无妨）
				if l.act("open", r.res.Session.AgentDeckID) == nil {
					selectCenter(layoutSock)
				}
				l.say(text, ok) // act 成功会清掉提示，补回来
			}
			l.refresh()
		case ev, ok := <-d.events:
			if !ok {
				return nil
			}
			list := sb.Data.Ordered()
			switch {
			case ev.Mouse != nil && ev.Mouse.Wheel != 0:
				sb.Move(ev.Mouse.Wheel)
			case ev.Mouse != nil && ev.Mouse.Press && ev.Mouse.Button == 0:
				if p, ok := sb.HitProject(ev.Mouse.Y); ok {
					sb.Proj = p // 点项目标题只选中，不起任何东西
				} else if i, eye, ok := sb.Hit(ev.Mouse.X, ev.Mouse.Y); ok {
					sb.Proj, sb.Sel = "", i
					if eye && sb.Data.IsOpen(list[i]) {
						l.act("hide", list[i].AgentDeckID)
					} else {
						open(i)
					}
				}
			case ev.Key == "up" || ev.Key == "k":
				sb.Move(-1)
			case ev.Key == "down" || ev.Key == "j":
				sb.Move(1)
			case ev.Key == "m":
				startMain(sb.SelectedProject())
			case ev.Key == "enter" || ev.Key == "o":
				if sb.Proj == "" {
					open(sb.Sel)
				}
			case ev.Key == "h":
				if sb.Proj == "" && sb.Sel < len(list) && sb.Data.IsOpen(list[sb.Sel]) {
					l.act("hide", list[sb.Sel].AgentDeckID)
				}
			case ev.Key == "esc" || ev.Key == "f10":
				selectCenter(layoutSock)
			}
		}
		draw()
	}
}

// RunTabs 跑标签条。
func RunTabs(api API, layoutSock string) error {
	d, err := openTerm()
	if err != nil {
		return err
	}
	defer d.close()
	tb := &Tabs{}
	l := newLoop(api)
	var raw Data
	last := ""
	draw := func() {
		tb.Data = l.merge(raw)
		w, h := size()
		f := tb.Render(w)
		if tb.Data.Err != "" {
			f = Frame{Lines: []string{Fit("\x1b[38;5;196m "+tb.Data.Err+"\x1b[0m", w)}}
		}
		if out := f.Encode(w, h); out != last {
			os.Stdout.WriteString(out)
			last = out
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case raw = <-l.data:
		case <-d.resize:
			last = ""
		case <-tick.C:
		case ev, ok := <-d.events:
			if !ok {
				return nil
			}
			switch {
			case ev.Key == "ctrl-q": // 布局 server 把 Ctrl-Q 转到这里：放回后台
				// 不带会话号：由 deckd 取它此刻的焦点（标签条手里的数据可能是一秒前的）
				l.act("hide", "")
				selectCenter(layoutSock)
			case ev.Key == "f2": // 布局 server 把根表上的 F2 转到这里
				l.act("takeover", "")
				selectCenter(layoutSock)
			case ev.Mouse != nil && ev.Mouse.Press && ev.Mouse.Button == 0:
				if id, closeIt, ok := tb.Hit(ev.Mouse.X); ok {
					if closeIt {
						l.act("hide", id)
					} else {
						l.act("focus", id)
					}
				}
				selectCenter(layoutSock)
			case ev.Key == "esc" || ev.Key == "f10":
				selectCenter(layoutSock)
			}
		}
		draw()
	}
}
