// Work Deck：把一个 Graph Engineering 项目的进度摆在一屏里。
//
// 本轮只读。它通过 projectctl 的只读子命令取状态，不派工、不接管会话、
// 不往事件账本写任何东西。取不到的数据一律显示为未知，不填看起来合理的数字。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

	if _, err := tea.NewProgram(ui.New(reader), tea.WithAltScreen()).Run(); err != nil {
		fail("界面异常退出：%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "work: "+format+"\n", args...)
	os.Exit(1)
}
