package deckui

import (
	"context"
	"errors"
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

// errText 把错误清洗成一行可显示的文字。
func errText(err error) string { return deckstore.Clean(err.Error(), 120) }

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
	errText  string
	errUntil time.Time
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
				d.Err = "连不上 deckd：" + errText(err)
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
		l.errText, l.errUntil = errText(err), time.Now().Add(6*time.Second)
	} else {
		l.errText = ""
	}
	l.refresh()
	return err
}

func (l *loop) merge(d Data) Data {
	if l.errText != "" && time.Now().Before(l.errUntil) && d.Err == "" {
		d.Err = l.errText
	}
	return d
}

// RunSidebar 跑左侧会话栏。
func RunSidebar(api API, layoutSock string) error {
	d, err := openTerm()
	if err != nil {
		return err
	}
	defer d.close()
	sb := &Sidebar{Mode: logo.Mode()}
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
	tick := time.NewTicker(time.Second) // 让出错提示按时消失
	defer tick.Stop()
	for {
		select {
		case raw = <-l.data:
		case <-d.resize:
			sb.CellW, sb.CellH = cellSize()
			last = ""
		case <-tick.C:
		case ev, ok := <-d.events:
			if !ok {
				return nil
			}
			list := sb.Data.Ordered()
			switch {
			case ev.Mouse != nil && ev.Mouse.Wheel != 0:
				sb.Sel = max(0, min(len(list)-1, sb.Sel+ev.Mouse.Wheel))
			case ev.Mouse != nil && ev.Mouse.Press && ev.Mouse.Button == 0:
				if i, eye, ok := sb.Hit(ev.Mouse.X, ev.Mouse.Y); ok {
					sb.Sel = i
					if eye && sb.Data.IsOpen(list[i]) {
						l.act("hide", list[i].AgentDeckID)
					} else {
						open(i)
					}
				}
			case ev.Key == "up" || ev.Key == "k":
				sb.Sel = max(0, sb.Sel-1)
			case ev.Key == "down" || ev.Key == "j":
				sb.Sel = min(len(list)-1, sb.Sel+1)
			case ev.Key == "enter" || ev.Key == "o":
				open(sb.Sel)
			case ev.Key == "h":
				if sb.Sel < len(list) && sb.Data.IsOpen(list[sb.Sel]) {
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
