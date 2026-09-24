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
	"github.com/Tingschen287/terminal-deck/work/internal/binding"
	"github.com/Tingschen287/terminal-deck/work/internal/config"
	"github.com/Tingschen287/terminal-deck/work/internal/dispatch"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
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
	dispatcher, dispatchErr := buildDispatcher(root)

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
		fmt.Print(ui.Render(snap, width))
		return
	}

	model := ui.New(reader)
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
	if _, err := tea.NewProgram(model, tea.WithAltScreen()).Run(); err != nil {
		fail("界面异常退出：%v", err)
	}
}

// buildDispatcher 按配置装配派工能力。
//
// 配置缺失不是错误，只是没有派工：Work Deck 照样能看。但配置写错了要说出来，
// 不能让人按了 d 之后对着一句没头没脑的提示发呆。
func buildDispatcher(projectRoot string) (ui.Dispatcher, error) {
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
	return &dispatch.Dispatcher{
		Deck:    client,
		Store:   store,
		Table:   table,
		LogPath: filepath.Join(filepath.Dir(path), "dispatch-log.jsonl"),
	}, nil
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

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "work: "+format+"\n", args...)
	os.Exit(1)
}
