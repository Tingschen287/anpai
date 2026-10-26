// Command deck 是「安排 Anpai」（原名 Agent Deck，adr_032）v1 的入口（n050 起）。
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
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Tingschen287/terminal-deck/work/internal/config"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/deckui"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
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
		idle(dirs)
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
	upstream := fs.String("upstream", os.Getenv("DECK_AGENTDECK_ADDR"), "agent-deck headless 服务地址（读会话运行状态）；留空则用 agent-deck list --json 读（n055，默认）")
	sysLang := fs.String("system-lang", "", "owner 没选过界面语言时用的系统语言（zh 或 en）；deck up 按自己的环境变量算好传进来，留空则按 deckd 自己的环境变量")
	fs.Parse(args)
	pc := *script
	if pc == "" {
		pc, _ = projectctl.DefaultScript() // 找不到时进度面板会说明
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// SessionPath：找 agent CLI 与起会话用同一份 PATH（登录 shell 的 PATH 与 deckd 自己的合并，n060 段 4，见 internal/sessionenv）。
	// 只在 deck daemon 里开；解析会运行 owner 的 shell 启动文件，测试里的 deckd.Run 不开
	cfg := deckd.Config{Dirs: dirs, Profile: *profile, LayoutSocket: *layout, AgentSocket: *agent, DashboardURL: *dash, Projectctl: pc, SessionPath: true,
		UpstreamAddr: *upstream, UpstreamToken: config.Token(), SystemLang: daemonSystemLang(*sysLang, os.Getenv)}
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
	c := uiClient(dirs)
	if args[0] == "quota" {
		// 额度面板要在每个来源前贴 logo（sixel），用与侧栏同一套整屏帧画法（n056）
		if err := deckui.RunQuota(c, func() string { return logo.CurrentMode(dirs.State) }); err != nil {
			fail("%v", err)
		}
		return
	}
	if err := panels.Run(args[0], *project, c); err != nil {
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

// 界面语言（n060 段 3）。
//
// 选择存在设置里（agents.yaml 的 ui_lang），deckd 是唯一的真相，每个响应带 X-Deck-Lang。界面进程（panel、sidebar、tabs、settings、
// center、idle）用 uiClient 建客户端：先按首次语言规则猜一个（deckstore.ResolveUILang：设置里有就用，老用户文件没有就中文，全新安装按自己的环境变量；
// deckd 连不上时用），再向 deckd 对一次，之后每次请求的响应都带语言，
// 变了就换（侧栏、标签条每秒取一次，面板每 2 秒，所以改完语言后 1～2 秒内各进程都换过去，不用重启）。
// deck agent、deck project、deck reports、whoami 这些命令行（给模型和 owner 的 shell）和 deckd、deck up 不跟：永远是中文。

// uiClient 建界面进程用的 deckd 客户端，并先对一次界面语言。
func uiClient(dirs deckstore.Dirs) *deckclient.Client {
	i18n.SetLang(deckstore.ResolveUILang(dirs.Config, os.Getenv))
	c := deckclient.NewUI(deckd.SocketPath(dirs)).FollowLang()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	c.SyncLang(ctx) // 连不上就保持先猜的；循环里每次请求还会再对
	return c
}

// daemonSystemLang 是 deckd 用的系统语言：deck up 传来的优先（它跑在 owner 的 shell 里，环境最可信）；
// 没传（手动 deck daemon）就按 deckd 自己的环境变量。deckd 跑在布局 tmux 的后台窗口里，那个环境来自第一次建布局的 shell，
// 可能和 owner 现在的 shell 不同，所以不靠它。
func daemonSystemLang(flagValue string, getenv func(string) string) string {
	if i18n.ValidLang(flagValue) {
		return flagValue
	}
	return i18n.SystemLang(getenv)
}

// displayWidth 是字符串在终端里占的列数（中文算两列）。
func displayWidth(s string) int { return lipgloss.Width(s) }

// ui：布局里的侧栏或标签条（n049）。
func ui(dirs deckstore.Dirs, which string, args []string) {
	fs := flag.NewFlagSet(which, flag.ExitOnError)
	layoutSock, _ := socketFlags(fs)
	fs.Parse(args)
	api := deckui.DeckAPI{C: uiClient(dirs)}
	var err error
	if which == "sidebar" {
		err = deckui.RunSidebar(api, *layoutSock, func() string { return logo.CurrentMode(dirs.State) })
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
	if err := deckui.RunSettings(deckui.DeckSettingsAPI{C: uiClient(dirs)}); err != nil {
		fail("%v", err)
	}
}
