package deckd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
	"github.com/Tingschen287/terminal-deck/work/internal/writerlock"
)

// SocketPath 是 deckd 的 socket 路径。
func SocketPath(d deckstore.Dirs) string { return filepath.Join(d.State, "deckd.sock") }

// LockPathForSocket 是 deckd 的单实例锁文件：与 socket 同目录。
func LockPathForSocket(sock string) string { return filepath.Join(filepath.Dir(sock), "deckd.lock") }

// LockState 是探测 deckd 单实例锁的结果。
type LockState int

const (
	LockMissing LockState = iota // 锁文件不存在：deckd 还没在这个状态目录起来过（或目录不对）
	LockFree                     // 锁空闲：没有 deckd 进程持有它（锁随 deckd 退出才释放，见 Run）
	LockHeld                     // 锁被持有：deckd 在运行（可能正在关闭，手上还有请求）
)

// ProbeLock 探测 deckd 的单实例锁，不创建锁文件。拿得到锁时立刻放开；deckd 启动时抢锁会稍等重试，
// 不会因为恰好撞上这一瞬而起不来（n049 评审 4：「deckd 没在运行」要以锁为准，不能只凭 socket 不在或连接被拒）。
func ProbeLock(path string) (LockState, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return LockMissing, nil
	}
	if err != nil {
		return LockMissing, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return LockHeld, nil
		}
		return LockMissing, err
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return LockFree, nil
}

// flockRetry 抢单实例锁；被占着时稍等重试（别的进程的 ProbeLock 只持锁一瞬），总共等 wait。
func flockRetry(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil || !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Run 启动 deckd，直到 ctx 结束。
//
// 启动顺序：状态目录 0700 → deckd 单实例锁（进程生命周期）→ profile 写入者锁（与旧 work 互斥，架构稿 §3）→
// 读状态 → 清掉残留 socket（锁在我们手上，残留的一定是上次没清干净的）→ 监听，socket 0600。
// center 为 nil 时不接布局（/v1/view 返回 503），由 deck up 启动时提供。
func Run(ctx context.Context, cfg Config, center viewstate.Center) error {
	if cfg.Profile == "" {
		cfg.Profile = "deck"
	}
	if err := os.MkdirAll(cfg.Dirs.State, 0o700); err != nil {
		return err
	}
	os.Chmod(cfg.Dirs.State, 0o700)
	lockPath := filepath.Join(cfg.Dirs.State, "deckd.lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := flockRetry(lf, 2*time.Second); err != nil {
		return fmt.Errorf("deckd 已经在运行（%s 被占用）", lockPath)
	}
	wl, err := writerlock.Acquire(cfg.Profile, "deckd")
	if err != nil {
		return fmt.Errorf("不启动：%w", err)
	}
	defer wl.Release()

	store, err := deckstore.Open(cfg.Dirs)
	if err != nil {
		return err
	}
	// 写入权记账：中间窗格的接管交还与开子 agent 后的自动派送都用它（n051 起总是打开）
	leases, err := leaseStore(cfg.Dirs)
	if err != nil {
		return err
	}
	defer leases.Close()
	var view *viewstate.Machine
	if center != nil {
		// 注册表 key 与 agent-deck id 都认，统一成 agent-deck id（lease 与派送用的就是它，n050 评审 B3）
		lookup := func(id string) (viewstate.Target, bool) {
			x, ok := store.Session(id)
			if !ok || x.TmuxSession == "" || x.AgentDeckID == "" {
				return viewstate.Target{}, false
			}
			return viewstate.Target{Session: x.AgentDeckID, Tmux: x.TmuxSession, Role: x.Role, BriefConfirmed: x.BriefConfirmed,
				AwaitingOwner: x.AwaitingOwner != "", Title: x.Title}, true
		}
		notify := func(text string) error {
			_, err := store.Notify("", "view", text, "deckd")
			return err
		}
		view = viewstate.New(center, leases, lookup, notify, store.Broken)
	}
	srv := NewServer(cfg, store,
		identity.Resolver{Sources: []identity.PaneSource{identity.TmuxSource{Socket: cfg.LayoutSocket}, identity.TmuxSource{Socket: cfg.AgentSocket}}},
		&dashquota.Client{URL: cfg.DashboardURL},
		view, graphProgress(cfg.Projectctl))
	if cfg.UpstreamAddr != "" {
		srv.activity = newActivityCache(upstreamActivity(cfg.Profile, cfg.UpstreamAddr, cfg.UpstreamToken))
	} else {
		srv.activity = newActivityCache(listActivity(cfg.agentDeckBinary(), cfg.Profile)) // n055：默认用 list --json，不用 token
	}
	go srv.activity.run(ctx)
	// 开子 agent、发话、转告主 agent（n051）：经 agent-deck 命令行
	if up, err := agentdeck.NewClient(cfg.agentDeckBinary(), cfg.Profile, cfg.UpstreamAddr, cfg.UpstreamToken); err == nil {
		var gs GraphStartFunc
		if cfg.Projectctl != "" {
			gs = projectctlGraphStart(cfg.Projectctl)
		}
		srv.WithAgents(up, leases, agentdeck.ToolInfo, gs)
		if cfg.Projectctl != "" {
			script := cfg.Projectctl
			srv.agents.graphState = func(ctx context.Context, p deckstore.ProjectDef, node string) (projectctl.State, error) {
				return projectctl.NewClient(p.GraphRoot, script).BriefState(ctx, node)
			}
		}
		// brief 里写 deck 的绝对路径：agent 的 shell 未必有 deck 所在的 PATH（Codex 用登录 shell）
		if exe, err := os.Executable(); err == nil && !strings.ContainsAny(exe, " \t'\"$`\\") {
			DeckCommand = exe
		}
		agentSock := cfg.AgentSocket
		srv.WithBypassChecks(BypassChecks{Config: agentdeck.CheckUpstreamConfigNoBypass, NotCodex: agentdeck.CustomToolNotCodex,
			Alive: func(ctx context.Context, tmux string) (bool, error) {
				return agentdeck.TmuxSessionAlive(ctx, agentSock, tmux)
			},
			Processes: func(ctx context.Context, tmux string, want agentdeck.LaunchArgs) error {
				return agentdeck.CheckSessionArgs(ctx, agentSock, tmux, want)
			}})
		// 汇报循环（含后台查账本）结束之后再释放写入者锁：Run 返回前取消并等它（attempt 2 自查）
		rctx, rcancel := context.WithCancel(ctx)
		srv.WithBackground(rctx)
		srv.ResumeAwaiting() // 重启前停在启动确认上的主 agent：接着等，处理完再送 brief（n055 自查）
		rdone := make(chan struct{})
		go func() { defer close(rdone); srv.runReports(rctx) }()
		defer func() { rcancel(); <-rdone; srv.WaitBackground() }()
	} else {
		store.Notify("", "deckd", "没接上 agent-deck，开子 agent 不可用："+err.Error(), "deckd")
	}

	sock := SocketPath(cfg.Dirs)
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理残留 socket 失败：%w", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return err
	}
	hs := &http.Server{Handler: srv.Handler(), ConnContext: withConn, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
		closeView(view)
		os.Remove(sock)
		return nil
	case err := <-errc:
		closeView(view)
		os.Remove(sock)
		return err
	}
}

// Progress 是进度面板的数据。
type Progress struct {
	Project       string         `json:"project"`
	Graph         bool           `json:"graph"`
	GraphRevision *int           `json:"graph_revision,omitempty"`
	Health        string         `json:"health,omitempty"`
	Counts        map[string]int `json:"counts"`
	Unknown       int            `json:"unknown"`
	Budgets       []BudgetView   `json:"budgets"`
	Problems      []string       `json:"problems,omitempty"`
	TakenAt       time.Time      `json:"taken_at"`
}

// BudgetView 是一行预算；读不到的维度 Used/Limit 为 nil，并给出原因。
type BudgetView struct {
	Label  string   `json:"label"`
	Used   *float64 `json:"used"`
	Limit  *float64 `json:"limit"`
	Unit   string   `json:"unit"`
	Reason string   `json:"reason,omitempty"`
}

// graphProgress 用 projectctl 的快速快照读进度，只走只读命令。
func graphProgress(script string) ProgressFunc {
	return func(ctx context.Context, p deckstore.ProjectDef) (any, error) {
		snap := projectctl.NewReader(p.GraphRoot, script).FastSnapshot(ctx)
		counts, unknown := snap.Counts()
		out := Progress{Project: p.ID, Graph: true, Counts: map[string]int{}, Unknown: unknown, Problems: snap.Problems, TakenAt: snap.TakenAt}
		for st, n := range counts {
			out.Counts[string(st)] = n
		}
		if v, ok := snap.GraphRevision.Get(); ok {
			out.GraphRevision = &v
		}
		if v, ok := snap.Health.Get(); ok {
			out.Health = v
		}
		for _, b := range snap.Budgets {
			bv := BudgetView{Label: b.Label, Unit: b.Unit}
			if v, ok := b.Used.Get(); ok {
				bv.Used = &v
			} else {
				bv.Reason = b.Used.Reason()
			}
			if v, ok := b.Limit.Get(); ok {
				bv.Limit = &v
			}
			out.Budgets = append(out.Budgets, bv)
		}
		return out, nil
	}
}

// closeView 在放单实例锁之前调用：Shutdown 超时后可能还有视图请求在跑，等它做完，之后的一律拒绝。
// 这样锁一空闲，就不可能再有操作作用到中间 client 上（deck center 的栅栏以此为准，n049 评审 4）。
func closeView(view *viewstate.Machine) {
	if view != nil {
		view.Close()
	}
}

// agentDeckBinary 是 agent-deck 可执行文件（环境变量 WORKDECK_AGENTDECK_BIN 可覆盖，与旧 work 一致）。
func (c Config) agentDeckBinary() string {
	if b := os.Getenv("WORKDECK_AGENTDECK_BIN"); b != "" {
		return b
	}
	return "agent-deck"
}
