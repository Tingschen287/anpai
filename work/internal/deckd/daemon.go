package deckd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/identity"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/viewstate"
	"github.com/Tingschen287/terminal-deck/work/internal/writerlock"
)

// SocketPath 是 deckd 的 socket 路径。
func SocketPath(d deckstore.Dirs) string { return filepath.Join(d.State, "deckd.sock") }

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
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
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
	var view *viewstate.Machine
	if center != nil {
		leases, err := leaseStore(cfg.Dirs)
		if err != nil {
			return err
		}
		defer leases.Close()
		lookup := func(session string) (viewstate.Target, bool) {
			x, ok := store.Session(session)
			if !ok || x.TmuxSession == "" {
				return viewstate.Target{}, false
			}
			return viewstate.Target{Session: x.AgentDeckID, Tmux: x.TmuxSession, Role: x.Role, BriefConfirmed: x.BriefConfirmed, Title: x.Title}, true
		}
		view = viewstate.New(center, leases, lookup, func(text string) { store.Notify("", "view", text, "deckd") })
	}
	srv := NewServer(cfg, store,
		identity.Resolver{Sources: []identity.PaneSource{identity.TmuxSource{Socket: cfg.LayoutSocket}, identity.TmuxSource{Socket: cfg.AgentSocket}}},
		&dashquota.Client{URL: cfg.DashboardURL},
		view, graphProgress(cfg.Projectctl))

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
		os.Remove(sock)
		return nil
	case err := <-errc:
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
