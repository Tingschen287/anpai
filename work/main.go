// Work Deck：把一个 Graph Engineering 项目的进度摆在一屏里，并能就地动手。
//
// 对 Graph 控制器只读：状态一律走 projectctl 的只读子命令，
// **不往事件账本写任何东西**——开工、提交、验收都由人在命令行上做。
// 取不到的数据一律显示为未知，不填看起来合理的数字。
//
// 对 upstream agent-deck 会写：派工要建会话，派评审要投递 brief，
// 进入会话要把终端交出去。哪条上游命令留下什么，
// internal/agentdeck/MANIFEST.md 有逐条的表，每条带上游源码依据。
//
// 这段注释在 n035 时写的是「本轮只读……不派工、不接管会话」，那时是对的；
// n036 加派工、n037 加进入与派评审之后就不对了，却一直留到 n037 的独立评审
// 才被扫出来。能力变了，说明也要跟着变。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/arbiter"
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/config"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
	"github.com/Tingschen287/terminal-deck/work/internal/queue"
	"github.com/Tingschen287/terminal-deck/work/internal/routing"
	"github.com/Tingschen287/terminal-deck/work/internal/ui"
)

func main() {
	var (
		project = flag.String("project", ".", "Graph 项目根目录")
		script  = flag.String("projectctl", "", "Graph 控制器脚本路径，留空则按工作区布局自动查找")
		once    = flag.String("once", "", "只渲染一次就退出，值是输出宽度，例如 100")
		fast    = flag.Bool("fast", false, "配合 --once：只用状态投影取数，不逐个问控制器，快但有节点会显示查询中")
	)
	flag.Parse()

	root, err := filepath.Abs(*project)
	if err != nil {
		fail("项目路径无法解析：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "work-graph.yaml")); err != nil {
		fail("%s 看起来不是 Graph 项目根：%v", root, err)
	}

	pc := *script
	if pc == "" {
		pc, err = projectctl.DefaultScript()
		if err != nil {
			fail("%v", err)
		}
	}

	reader := projectctl.NewReader(root, pc)
	// 终态问过一次就记到盘上：真实项目首次要问近两分钟，第二次开就该是秒出。
	if cacheDir, err := config.DefaultPath(); err == nil {
		reader = reader.WithCache(filepath.Join(filepath.Dir(cacheDir), "node-states.json"))
	}

	// 派工能力是可选的：没配好就只看不派，按 d 时界面会说清楚缺什么。
	// 写入权记账只开一份：派工、决策队列、进入会话共用同一个实例，内存与磁盘才一致。
	leases, leasesErr := openLeases()
	// 额度观测要看决策队列；队列在下面才打开，这里给派工器一个现取的口子。
	var qstore *queue.Store
	queueItems := func() []queue.Item {
		if qstore == nil {
			return nil
		}
		return qstore.Items()
	}
	dispatcher, dispatchErr := buildDispatcher(root, leases, queueItems)

	if *once != "" {
		width := 100
		if _, err := fmt.Sscanf(*once, "%d", &width); err != nil {
			fail("--once 需要一个宽度数字：%v", err)
		}
		// 节点多的项目要逐个问控制器，给足时间，别中途被砍。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var snap projectctl.Snapshot
		if *fast {
			snap = reader.FastSnapshot(ctx)
		} else {
			snap = reader.Snapshot(ctx)
		}
		fmt.Print(renderOnce(ctx, snap, pc, width))
		return
	}

	model := ui.New(reader)
	if leasesErr != nil {
		model = model.WithLeasesError(leasesErr)
	} else {
		model = model.WithLeases(leases)
	}
	if dispatcher != nil {
		model = model.WithDispatcher(dispatcher)
	} else if dispatchErr != nil {
		fmt.Fprintf(os.Stderr, "work: 派工未启用：%v\n", dispatchErr)
	}
	if attacher, bindings, err := buildAttacher(); err == nil {
		model = model.WithAttacher(attacher, bindings)
	} else if dispatchErr == nil {
		// 派工配好了却进不去，说明配置有别的毛病，要说出来。
		fmt.Fprintf(os.Stderr, "work: 进入会话未启用：%v\n", err)
	}
	// 决策队列：Graph 的门总能接上；上游没配好时照样开，界面会明说运行时来源取不到。
	// 队列文件打不开时不静默退掉：界面常驻告警。
	if store, up, err := buildQueue(); err == nil {
		qstore = store
		if up != nil {
			model = model.WithQueue(store, up, arbiter.Deliverer{Client: up, Leases: leases}, pc)
		} else {
			model = model.WithQueue(store, nil, nil, pc)
		}
	} else {
		model = model.WithQueueError(err)
	}
	if _, err := tea.NewProgram(model, tea.WithAltScreen()).Run(); err != nil {
		fail("界面异常退出：%v", err)
	}
}

// buildDispatcher 按配置装配派工能力。
//
// 配置缺失不是错误，只是没有派工：Work Deck 照样能看。但配置写错了要说出来，
// 不能让人按了 d 之后对着一句没头没脑的提示发呆。
func buildDispatcher(projectRoot string, leases *lease.Store, queueItems func() []queue.Item) (ui.Dispatcher, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	table, err := cfg.RouteTable()
	if err != nil {
		return nil, err
	}
	// token 只从环境变量来，配置文件里没有这个字段。
	client, err := agentdeck.NewClient(cfg.UpstreamBinary, cfg.Profile, cfg.UpstreamAddr, config.Token())
	if err != nil {
		return nil, err
	}
	store, err := binding.Open(filepath.Join(filepath.Dir(path), "bindings.json"))
	if err != nil {
		return nil, err
	}
	modelOf := func(sessionID string) (string, bool) {
		b, ok := store.BySession(sessionID)
		return b.Model, ok
	}
	return &dispatch.Dispatcher{
		Deck:       arbiter.Deck{Client: client, Leases: leases, Clients: client},
		Store:      store,
		Table:      table,
		LogPath:    filepath.Join(filepath.Dir(path), "dispatch-log.jsonl"),
		NodeCaps:   cfg.NodeCapabilities,
		Quota:      routing.Live{Items: queueItems, ModelOf: modelOf},
		RoutingLog: filepath.Join(filepath.Dir(path), "routing-decisions.jsonl"),
	}, nil
}

// openLeases 打开写入权记账（配置目录的 leases.json；持有者与切换事件同存这一个文件）。
//
// 打不开（例如文件损坏）时返回错误：界面常驻告警，派工与队列拿到的记账是 nil，一律不投递。
func openLeases() (*lease.Store, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	return lease.Open(filepath.Join(filepath.Dir(path), "leases.json"))
}

// buildAttacher 装配「进入会话」与「预览」能力，外加绑定表。
//
// 与派工共用同一份配置和同一个绑定存储：进得去的前提是这次 attempt 是从
// Work Deck 派出去的，否则根本不知道该进哪个会话。
func buildAttacher() (ui.Attacher, ui.Bindings, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	client, err := agentdeck.NewClient(cfg.UpstreamBinary, cfg.Profile, cfg.UpstreamAddr, config.Token())
	if err != nil {
		return nil, nil, err
	}
	store, err := binding.Open(filepath.Join(filepath.Dir(path), "bindings.json"))
	if err != nil {
		return nil, nil, err
	}
	return client, store, nil
}

// buildQueue 打开决策队列，并在配置可用时接上 upstream agent-deck。
//
// 队列文件放在配置目录的 queue.json。上游配置加载失败不算错：返回的 client 为 nil，
// 界面上队列照开，只含 Graph 的门，并常驻告警「运行时来源取不到」。
// 队列文件本身打不开（例如损坏）才算错：不能当成空队列，丢掉已回答的记录。
func buildQueue() (*queue.Store, *agentdeck.Client, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	store, err := queue.Open(filepath.Join(filepath.Dir(path), "queue.json"))
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return store, nil, nil
	}
	client, err := agentdeck.NewClient(cfg.UpstreamBinary, cfg.Profile, cfg.UpstreamAddr, config.Token())
	if err != nil {
		return store, nil, nil
	}
	return store, client, nil
}

// renderOnce 是 --once 的输出：Graph 表盘加决策队列的告警与条数。
func renderOnce(ctx context.Context, snap projectctl.Snapshot, script string, width int) string {
	q, open := onceQueue(ctx, snap, script)
	return ui.RenderWithQueue(snap, width, q, open)
}

// onceQueue 给 --once 走一遍队列：并入 Graph 门，自检后读一次 export，返回要画的内容。
func onceQueue(ctx context.Context, snap projectctl.Snapshot, script string) (ui.QueueScreen, int) {
	store, client, err := buildQueue()
	if err != nil {
		return ui.QueueSnapshot(nil, queue.Intake{}, err)
	}
	now := time.Now()
	var b queue.Bindings
	if _, bs, err := buildAttacher(); err == nil {
		b = bs
	}
	if _, _, err := queue.MergeGraph(store, snap, b, script, now); err != nil {
		return ui.QueueSnapshot(nil, queue.Intake{}, err)
	}
	var up queue.Upstream
	if client != nil {
		up = client
	}
	in, recs := queue.Poll(ctx, up, now)
	if in.Configured && in.ExportErr == "" {
		if _, err := store.Merge(queue.SourceSession, queue.FromInbox(recs, now), now); err != nil {
			return ui.QueueSnapshot(nil, queue.Intake{}, err)
		}
	}
	return ui.QueueSnapshot(store, in, nil)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "work: "+format+"\n", args...)
	os.Exit(1)
}
