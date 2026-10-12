// Command deck 是 Agent Deck v1 的入口（n050 起）。
//
//	deck up                     打开四区布局（没有 deckd 就在布局后台窗口里起它）
//	deck daemon                 启动后台服务 deckd
//	deck panel <名字> [-project] 在当前窗格里跑一个面板（decisions、quota、progress、notifications、activity）
//	deck whoami                 deckd 把当前进程认成谁
//	deck agent …                主 agent 与子 agent 的工具（n051，见 agent.go）
//	deck project add …          owner 新建项目（n051）
//
// 布局窗格里的程序（n049）：center、sidebar、tabs、settings、idle、layout。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/config"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/deckui"
	"github.com/Tingschen287/terminal-deck/work/internal/panels"
	"github.com/Tingschen287/terminal-deck/work/internal/projectctl"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	dirs, err := deckstore.DefaultDirs()
	if err != nil {
		fail("%v", err)
	}
	switch os.Args[1] {
	case "daemon":
		daemon(dirs, os.Args[2:])
	case "up":
		up(dirs, os.Args[2:])
	case "center":
		center(dirs, os.Args[2:])
	case "idle":
		idle()
	case "layout":
		layoutCmd(os.Args[2:])
	case "sidebar", "tabs":
		ui(dirs, os.Args[1], os.Args[2:])
	case "settings":
		settings(dirs, os.Args[2:])
	case "panel":
		panel(dirs, os.Args[2:])
	case "agent":
		agentCmd(dirs, os.Args[2:])
	case "reports":
		reportsCmd(dirs, os.Args[2:])
	case "project":
		projectCmd(dirs, os.Args[2:])
	case "whoami":
		var out map[string]any
		if err := deckclient.New(deckd.SocketPath(dirs)).Get(context.Background(), "/v1/whoami", &out); err != nil {
			fail("%v", err)
		}
		json.NewEncoder(os.Stdout).Encode(out)
	default:
		usage()
	}
}

// tmuxSocket 是 tmux 在 TMUX_TMPDIR 下给某个 -L 名字用的 socket 路径。
func tmuxSocket(name string) string {
	base := os.Getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}

func daemon(dirs deckstore.Dirs, args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	profile := fs.String("profile", "deck", "deck 使用的 agent-deck profile")
	layout := fs.String("layout-socket", tmuxSocket("deck"), "布局 tmux server 的 socket")
	agent := fs.String("agent-socket", tmuxSocket("default"), "agent-deck tmux server 的 socket")
	dash := fs.String("dashboard", "http://127.0.0.1:8899", "Token 看板地址")
	script := fs.String("projectctl", "", "Graph 控制器脚本路径，留空按工作区布局查找")
	upstream := fs.String("upstream", os.Getenv("DECK_AGENTDECK_ADDR"), "agent-deck headless 服务地址（读会话运行状态），留空则状态显示未知")
	fs.Parse(args)
	pc := *script
	if pc == "" {
		pc, _ = projectctl.DefaultScript() // 找不到时进度面板会说明
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := deckd.Config{Dirs: dirs, Profile: *profile, LayoutSocket: *layout, AgentSocket: *agent, DashboardURL: *dash, Projectctl: pc,
		UpstreamAddr: *upstream, UpstreamToken: config.Token()}
	fmt.Fprintf(os.Stderr, "deckd：%s（profile %s）\n", deckd.SocketPath(dirs), *profile)
	if err := deckd.Run(ctx, cfg, deckd.NewTmuxCenter(*layout, *agent)); err != nil {
		fail("%v", err)
	}
}

func panel(dirs deckstore.Dirs, args []string) {
	if len(args) < 1 {
		fail("用法：deck panel <%v> [-project id]", panels.Names)
	}
	fs := flag.NewFlagSet("panel", flag.ExitOnError)
	project := fs.String("project", "", "只看这个项目")
	fs.Parse(args[1:])
	if err := panels.Run(args[0], *project, deckclient.New(deckd.SocketPath(dirs))); err != nil {
		fail("%v", err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法：deck up | deck daemon | deck panel <名字> [-project id] | deck whoami\n"+
		"（布局窗格里的程序：deck center、deck sidebar、deck tabs、deck settings、deck idle、deck layout）")
	os.Exit(2)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "deck: "+format+"\n", a...)
	os.Exit(1)
}

// displayWidth 是字符串在终端里占的列数（中文算两列）。
func displayWidth(s string) int { return lipgloss.Width(s) }

// ui：布局里的侧栏或标签条（n049）。
func ui(dirs deckstore.Dirs, which string, args []string) {
	fs := flag.NewFlagSet(which, flag.ExitOnError)
	layoutSock, _ := socketFlags(fs)
	fs.Parse(args)
	api := deckui.DeckAPI{C: deckclient.New(deckd.SocketPath(dirs))}
	var err error
	if which == "sidebar" {
		err = deckui.RunSidebar(api, *layoutSock)
	} else {
		err = deckui.RunTabs(api, *layoutSock)
	}
	if err != nil {
		fail("%v", err)
	}
}

// settings：agent 设置界面（布局里按 F9）；-check 只读自查设置文件（升级前用，不需要 deckd）。
func settings(dirs deckstore.Dirs, args []string) {
	fs := flag.NewFlagSet("settings", flag.ExitOnError)
	check := fs.Bool("check", false, "只读检查 "+dirs.Config+" 下的设置文件，不写任何东西")
	socketFlags(fs)
	fs.Parse(args)
	if *check {
		if err := deckstore.CheckSettings(dirs.Config); err != nil {
			fail("设置有问题：%v", err)
		}
		fmt.Println("设置没有问题：" + dirs.Config)
		return
	}
	if err := deckui.RunSettings(deckui.DeckSettingsAPI{C: deckclient.New(deckd.SocketPath(dirs))}); err != nil {
		fail("%v", err)
	}
}
