package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/deckui"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
	"github.com/Tingschen287/terminal-deck/work/internal/termprobe"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
)

// socketFlags 是布局与 agent-deck 两个 tmux server 的 socket 参数（deck up 传给每个窗格里的子命令）。
func socketFlags(fs *flag.FlagSet) (layoutSock, agentSock *string) {
	return fs.String("layout-socket", tmuxSocket("deck"), "布局 tmux server 的 socket"),
		fs.String("agent-socket", tmuxSocket("default"), "agent-deck tmux server 的 socket")
}

// probeTerminal 问外层终端格子像素与 sixel 支持（n056）。不是终端时返回空结果。
func probeTerminal() (termprobe.Result, bool) {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return termprobe.Result{}, false
	}
	r := termprobe.Query(os.Stdin, time.Second)
	native := false
	if ws, err := termprobe.Winsize(int(os.Stdin.Fd())); err == nil && ws.Xpixel > 0 && ws.Ypixel > 0 {
		native = true
	}
	return r, native
}

// keepPixels 在 tmux 客户端接上后把格子像素补进自己的终端，窗口每次改大小（WSL 会清零）后再补，
// 补上后请布局 server 整屏重画，把已画成占位的图换回来（n056）。返回停止函数。格子像素未知时什么都不做。
func keepPixels(layoutSock string, cellW, cellH int) func() {
	if cellW <= 0 || cellH <= 0 || !term.IsTerminal(os.Stdin.Fd()) {
		return func() {}
	}
	fd := int(os.Stdin.Fd())
	tty, _ := os.Readlink("/proc/self/fd/0")
	winch := make(chan os.Signal, 4)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	fix := func() {
		if wrote, _ := termprobe.FixPixels(fd, cellW, cellH); wrote && tty != "" {
			// 写入后 tmux 客户端收到 SIGWINCH 才去重读；稍等再请重画，免得 server 还按旧尺寸画
			time.Sleep(250 * time.Millisecond)
			agentdeck.LayoutRefreshClient(context.Background(), layoutSock, tty)
		}
	}
	go func() {
		// 刚接上时客户端可能还没读尺寸：前几秒多补几次（已有像素就什么都不做）
		retry := time.NewTicker(400 * time.Millisecond)
		defer retry.Stop()
		for n := 0; ; {
			select {
			case <-done:
				return
			case <-winch:
				fix()
			case <-retry.C:
				if n++; n <= 8 {
					fix()
				}
			}
		}
	}()
	return func() { signal.Stop(winch); close(done) }
}

// up：一条命令打开四区布局（n049）。已经有布局就直接接上。
func up(dirs deckstore.Dirs, args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	layoutSock, agentSock := socketFlags(fs)
	graphics := fs.String("graphics", os.Getenv("DECK_GRAPHICS"), "logo 画法：sixel、blocks、none；留空自动判断（问终端：支持 sixel 且知道格子像素用 sixel，否则用字母徽章）")
	upstream := fs.String("upstream", os.Getenv("DECK_AGENTDECK_ADDR"), "agent-deck headless 服务地址（会话运行状态）；留空则 deckd 用 agent-deck list --json 读（n055，默认）")
	fs.Parse(args)
	self, err := os.Executable()
	if err != nil {
		fail("找不到 deck 自己的路径：%v", err)
	}
	ctx := context.Background()
	probe, native := probeTerminal()
	mode := termprobe.Mode(strings.ToLower(*graphics), probe, native)
	spec := layout.Spec{Deck: self, LayoutSocket: *layoutSock, AgentSocket: *agentSock, Graphics: mode}
	// 记下本次画法（在建布局之前：新起的窗格一开始就读到这次的，不先读到上次留下的；n056 评审 1 B2、attempt 2 自查）。
	// 最后接上的说了算：退出或失败时都不恢复（n056 评审 2 R1、owner rev 124 选简化方案）。按字符串恢复分不清
	// 文件是不是自己写的，较早的客户端退出会把后来者的画法改掉
	if err := logo.WriteMode(dirs.State, mode); err != nil {
		fmt.Fprintf(os.Stderr, "deck：记不下画法（已在运行的面板不会切换）：%v\n", err)
	}
	if w, h, err := term.GetSize(os.Stdout.Fd()); err == nil {
		spec.Width, spec.Height = w, h
	}
	// tmux 用 -S 指定 socket 时不会自建 tmux-UID 这层目录（用 -L 时才建）：先按 tmux 的做法建好，0700
	for _, sock := range []string{*layoutSock, *agentSock} {
		if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
			fail("建 tmux socket 目录失败：%v", err)
		}
	}
	// 空白占位会话：中间窗格起点与「全部隐藏」时显示它
	if err := agentdeck.EnsureSession(ctx, *agentSock, viewstate.Idle, layout.Quote(self)+" idle"); err != nil {
		fail("%v", err)
	}
	if !agentdeck.LayoutHasSession(ctx, *layoutSock, "deck") {
		if err := buildLayout(dirs, spec); err != nil {
			fail("%v", err)
		}
	}
	// 每次 up 都查一次 deckd：布局还在而 deckd 单独退出了，再运行 deck up 就把它重新起来（n049 评审 1 S1）
	if err := ensureDeckd(dirs, spec, *upstream); err != nil {
		fail("%v", err)
	}
	if mode == "sixel" {
		// 复用的布局可能是用别的画法起的：先让布局 server 认 sixel，再接上（只对之后接上的客户端生效）
		if err := agentdeck.LayoutEnsureSixel(ctx, *layoutSock); err != nil {
			// 打不开就退回字母徽章：sixel 特性不在时 tmux 对所有客户端画占位（n056 attempt 2 自查）
			fmt.Fprintf(os.Stderr, "deck：打开布局的 sixel 支持失败，改用字母徽章：%v\n", err)
			mode = "none"
			if err := logo.WriteMode(dirs.State, mode); err != nil {
				fmt.Fprintf(os.Stderr, "deck：记不下画法（已在运行的面板可能仍按 sixel 画，显示成占位）：%v\n", err)
			}
		}
	}
	cmd := agentdeck.LayoutAttachCommand(*layoutSock, "deck")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fail("接不上布局：%v", err)
	}
	stop := func() {}
	if mode == "sixel" {
		// 不看 native：同一个终端里第二次 deck up 时，读到的像素可能是上次自己补的（WSL 改大小会再清零）；
		// 终端真自己报像素时 FixPixels 什么都不写（n056 自查）
		stop = keepPixels(*layoutSock, probe.CellW, probe.CellH)
	}
	err = cmd.Wait()
	stop()
	if err != nil {
		fail("接不上布局：%v", err)
	}
}

func buildLayout(dirs deckstore.Dirs, spec layout.Spec) error {
	panels, err := deckstore.LoadPanels(dirs.Config)
	if err != nil {
		return fmt.Errorf("读面板注册表失败：%w", err)
	}
	spec.Panels = panels
	if err := os.MkdirAll(dirs.State, 0o700); err != nil {
		return err
	}
	base := filepath.Join(dirs.State, "layout.conf")
	if err := os.WriteFile(base, []byte(layout.BaseConf(spec)), 0o600); err != nil {
		return err
	}
	m := layout.TmuxMux{Socket: spec.LayoutSocket}
	built, err := layout.Build(m, spec, base)
	if err != nil {
		return fmt.Errorf("拼布局失败：%w", err)
	}
	keys := filepath.Join(dirs.State, "keys.conf")
	if err := os.WriteFile(keys, []byte(layout.BindingsConf(spec, built)), 0o600); err != nil {
		return err
	}
	if err := m.Source(keys); err != nil {
		return fmt.Errorf("装按键失败：%w", err)
	}
	return nil
}

// ensureDeckd：deckd 没在跑就在布局的后台窗口里起它（外部命令只许在 adapter 包里调，所以不在这里另起进程）。
// 后果：关掉布局 server 时 deckd 随之停止，agent 会话照常在 agent-deck server 上跑；再 deck up 时重新起来，
// 接管中的会话按 n050 的重启恢复处理（保持接管、自动派送暂停，等 owner 交还）。
func ensureDeckd(dirs deckstore.Dirs, spec layout.Spec, upstream string) error {
	var health map[string]any
	if deckclient.New(deckd.SocketPath(dirs)).Get(context.Background(), "/v1/health", &health) != nil {
		d := []string{layout.Quote(spec.Deck), "daemon", "-layout-socket", layout.Quote(spec.LayoutSocket), "-agent-socket", layout.Quote(spec.AgentSocket)}
		if upstream != "" {
			d = append(d, "-upstream", layout.Quote(upstream))
		}
		if err := agentdeck.LayoutBackgroundWindow(context.Background(), spec.LayoutSocket, "deck", "deckd", strings.Join(d, " ")); err != nil {
			return fmt.Errorf("起 deckd 失败：%w", err)
		}
	}
	return nil
}

// center：中间窗格里的重连循环（架构稿 §1、§7）。
func center(dirs deckstore.Dirs, args []string) {
	fs := flag.NewFlagSet("center", flag.ExitOnError)
	_, agentSock := socketFlags(fs)
	fs.Parse(args)
	self, _ := os.Executable()
	client := deckclient.New(deckd.SocketPath(dirs))
	in := bufio.NewReader(os.Stdin)
	loop := &layout.CenterLoop{
		Attach: func() (int, func() error, error) {
			agentdeck.EnsureSession(context.Background(), *agentSock, viewstate.Idle, layout.Quote(self)+" idle")
			cmd := agentdeck.CenterAttachCommand(*agentSock, viewstate.Idle)
			// tmux 的报错同时留一份：停止重试时把它写进说明（否则被清屏冲掉，只剩一个退出码）
			var errBuf bytes.Buffer
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, &errBuf)
			if err := cmd.Start(); err != nil {
				return 0, nil, err
			}
			return cmd.Process.Pid, func() error {
				err := cmd.Wait()
				if msg := strings.TrimSpace(errBuf.String()); err != nil && msg != "" {
					lines := strings.Split(msg, "\n")
					err = fmt.Errorf("%w（%s）", err, deckstore.Clean(lines[len(lines)-1], 160))
				}
				return err
			}, nil
		},
		Announce: func(ctx context.Context, op string, pid int) {
			announce(ctx, client, filepath.Join(dirs.State, "center.log"), *agentSock, op, pid)
		},
		Fence: func() {
			fence(client, filepath.Join(dirs.State, "center.log"), func(msg string) {
				fmt.Print("\x1b[2J\x1b[H\n  " + msg + "\n")
			})
		},
		Pause: func(reason string) bool {
			fmt.Print("\x1b[2J\x1b[H")
			fmt.Printf("\n  %s\n\n  检查 agent-deck 的 tmux server（%s）后按回车重试，输入 q 回车退出。\n", reason, *agentSock)
			line, _ := in.ReadString('\n')
			return strings.TrimSpace(line) != "q"
		},
	}
	loop.Run()
}

// fence 在起新的中间 client 之前，确认 deckd 没有做到一半的视图操作（n049 评审 3 B1-A）：
//   - deckd 回了 fence：它拿到过一次状态机的锁，之前的操作都做完了；
//   - deckd 根本没在运行（连接被拒、socket 不存在）：不可能有进行中的操作；
//   - 其他情况（超时、deckd 忙、答非所问）都不能证明操作已结束：不起新 client，在中间显示原因，每秒再试。
func fence(c *deckclient.Client, logPath string, show func(string)) {
	lock := deckd.LockPathForSocket(c.Socket)
	for i := 0; ; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), fenceTimeout)
		var out struct {
			Fenced bool `json:"fenced"`
		}
		err := c.Do(ctx, "POST", "/v1/view", map[string]any{"op": "fence"}, &out)
		cancel()
		if err == nil && out.Fenced {
			return
		}
		var why string
		switch {
		case err == nil:
			why = "deckd 的回答没有确认（fenced 不是 true）"
		default:
			// 连不上不等于没有进行中的操作：deckd 关闭时先关监听、再等手上的请求。以单实例锁为准（n049 评审 4）
			st, perr := deckd.ProbeLock(lock)
			switch {
			case perr == nil && st == deckd.LockFree:
				centerLog(logPath, "deckd 没在运行（单实例锁已释放），不用等进行中的操作：%v", err)
				return
			case perr != nil:
				why = fmt.Sprintf("探测 deckd 单实例锁失败：%v（%v）", perr, err)
			case st == deckd.LockHeld:
				why = fmt.Sprintf("deckd 的单实例锁还被持有（可能正在关闭、手上还有请求）（%v）", err)
			default:
				why = fmt.Sprintf("deckd 还没起来（%s 不存在）（%v）", lock, err)
			}
		}
		if i == 0 {
			centerLog(logPath, "重连前等 deckd 确认没有进行中的操作，还没确认：%s（不起新 client，每秒再试）", why)
		}
		show("等 deckd 确认没有进行中的操作再重连中间窗格…（" + deckstore.Clean(why, 160) + "）")
		time.Sleep(fenceRetry)
	}
}

// 栅栏每次等多久、没确认时隔多久再试（测试可调小）。
var fenceTimeout, fenceRetry = 5 * time.Second, time.Second

// centerLog 往 center.log 记一行（client 连着时界面看不到 deck center 的输出）。
func centerLog(logPath, format string, a ...any) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("2006-01-02 15:04:05 ")+format+"\n", a...)
}

// announce 等新 client 出现在 agent-deck server 上，再告诉 deckd（deckd 可能还在启动：重试 30 秒）。
// 结果记进 $XDG_STATE_HOME/deck/center.log：client 已连上、界面看不到这里的输出，出错要有地方查。
//
// 通知只属于这一个 client（n049 评审 1 B1）：它退出（ctx 取消）就不再发；每次发之前重新确认它还在；
// 请求带上它的进程号，deckd 在状态机锁里核对它仍是中间现在的 client，不是就拒绝（409），
// 所以查与发之间它恰好退出，也不会作用到新 client 上。
func announce(ctx context.Context, c *deckclient.Client, logPath, agentSock, op string, pid int) {
	logf := func(format string, a ...any) { centerLog(logPath, format, a...) }
	deadline := time.Now().Add(30 * time.Second)
	listed, seen := false, false
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			logf("client %d 已退出，不再通知 deckd（%s）", pid, op)
			return
		}
		list, lerr := agentdeck.ListTmuxClients(ctx, agentSock)
		listed = false
		for _, cl := range list {
			if cl.PID == pid {
				listed, seen = true, true
			}
		}
		if lerr == nil && seen && !listed {
			logf("client %d 已不在 agent-deck server 上，不再通知 deckd（%s）", pid, op)
			return
		}
		if listed {
			var out any
			err := c.Do(ctx, "POST", "/v1/view", map[string]any{"op": op, "client": pid}, &out)
			if err == nil {
				logf("client %d 已连上，%s 完成", pid, op)
				return
			}
			if de, ok := err.(*deckclient.Error); ok && de.Status != 503 {
				logf("client %d 已连上，%s 被 deckd 拒绝：%v", pid, op, err)
				return // deckd 明确拒绝（例如交还失败 409）：不重发，状态由面板与通知显示
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	logf("client %d：30 秒内没能通知 deckd（%s；client 已列出：%v）", pid, op, seen)
}

// idleLines 是空闲页的文字（第一行是产品名，窄时只写「Anpai」，n058）。
func idleLines(w int) []string {
	lines := []string{
		deckui.BrandFor(w),
		"",
		"左栏点一个会话，它就显示在这里；会话放回后台照常运行。",
		"",
	}
	for _, k := range layout.Keys {
		lines = append(lines, fmt.Sprintf("%-6s %s", k.Key, k.What))
	}
	return lines
}

// idle：空白占位画面（agent-deck server 上的 deck-idle 会话里跑它）。
func idle() {
	draw := func() {
		w, h, err := term.GetSize(os.Stdout.Fd())
		if err != nil {
			w, h = 80, 24
		}
		// 空闲页在 agent-deck 的嵌套 tmux 里，画不了 sixel：deck logo 用 16×8 格的半格色块（16×16 点）画在标题上方（n056）
		var art []string
		if im := logo.Deck(); im != nil && h >= 12+8+2 && w >= 20 {
			art = logo.Blocks(im, 16, 8) // 高度放不下就不画，免得挤掉按键说明（n056 自查）
		}
		lines := idleLines(w)
		fmt.Print("\x1b[2J\x1b[?25l")
		top := max(1, (h-len(lines)-len(art)-1)/2)
		for i, l := range art {
			fmt.Printf("\x1b[%d;%dH%s\x1b[0m", top+i, max(1, (w-16)/2), l)
		}
		if len(art) > 0 {
			top += len(art) + 1
		}
		for i, l := range lines {
			col := max(1, (w-displayWidth(l))/2)
			style := "\x1b[2m"
			if i == 0 {
				style = "\x1b[1;38;2;180;245;70m"
			}
			fmt.Printf("\x1b[%d;%dH%s%s\x1b[0m", top+i, col, style, l)
		}
	}
	draw()
	// 换上新二进制后自己换成新的（n058：owner 重开布局后空闲页还是旧名字——deck-idle 在 agent-deck 的 server 上，
	// 关掉布局 server 不会重起它，deck up 也只在没有时才建）。不从外面杀它重起：适配层禁止 kill、respawn（agentdeck 包的禁令）。
	self, _ := os.Executable()
	var started os.FileInfo
	if self != "" {
		started, _ = os.Stat(self)
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case s := <-sig:
			if s != syscall.SIGWINCH {
				return
			}
			draw()
		case <-tick.C:
			if exeReplaced(self, started) {
				signal.Stop(sig)
				syscall.Exec(self, os.Args, os.Environ()) // 成功就不回来；失败（新文件还没写完等）照旧跑
				signal.Notify(sig, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM)
				started, _ = os.Stat(self) // 同一个文件不再反复试
			}
		}
	}
}

// exeReplaced：启动时的二进制路径现在指向另一个文件（安装是写新文件再改名换上），且新文件是可执行的普通文件。
func exeReplaced(path string, started os.FileInfo) bool {
	if path == "" || started == nil {
		return false
	}
	now, err := os.Stat(path)
	return err == nil && now.Mode().IsRegular() && now.Mode().Perm()&0o111 != 0 && !os.SameFile(started, now)
}

// layoutCmd：deck layout toggle <left|right|bottom> | deck layout normalize（布局 server 的按键调用）。
func layoutCmd(args []string) {
	if len(args) < 1 {
		fail("用法：deck layout toggle <left|right|bottom> | deck layout normalize")
	}
	fs := flag.NewFlagSet("layout", flag.ExitOnError)
	layoutSock, agentSock := socketFlags(fs)
	sub, rest := args[0], args[1:]
	region := ""
	if sub == "toggle" {
		if len(rest) < 1 {
			fail("要显隐哪一栏：left、right、bottom")
		}
		region, rest = rest[0], rest[1:]
	}
	fs.Parse(rest)
	self, _ := os.Executable()
	spec := layout.Spec{Deck: self, LayoutSocket: *layoutSock, AgentSocket: *agentSock}
	m := layout.TmuxMux{Socket: *layoutSock}
	unlock, err := layout.Lock(*layoutSock+".lock", 10*time.Second)
	if err != nil {
		fail("%v", err)
	}
	defer unlock()
	switch sub {
	case "toggle":
		err = layout.Toggle(m, spec, region)
	case "normalize":
		err = layout.Normalize(m, spec)
	default:
		fail("不认识 %q", sub)
	}
	if err != nil {
		fail("%v", err)
	}
}
