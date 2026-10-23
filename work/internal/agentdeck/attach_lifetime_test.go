package agentdeck

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// 交互式 attach 命令不许跟着解析用的 context 一起死。
//
// 这条测试盯的是 attempt 9 的回归：给按 T 加解析超时时，20 秒的 context 被
// exec.CommandContext 绑到了 attach 命令上。`defer cancel()` 在造完命令返回时
// 就执行，而命令要等界面挂起自己之后才启动——于是解析越顺利，失败得越准时。
//
// 表现出来是「直连解析成功，进入时 context canceled」，重试也一样。
// 所以这里不测超时，测的是**最普通的那条路**：解析完、context 释放、命令照样能跑。
func TestInteractiveAttachCommandsOutliveTheResolveContext(t *testing.T) {
	// true 在 Linux 是 /bin/true，在 macOS 是 /usr/bin/true：按 PATH 找，不写死路径
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("找不到 true：", err)
	}
	c, err := NewClient(truePath, "_test", "", "")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		make func(ctx context.Context) (*exec.Cmd, error)
	}{
		{"session attach", func(ctx context.Context) (*exec.Cmd, error) {
			return c.AttachCommand(ctx, "sess-1")
		}},
		{"直连 tmux", func(ctx context.Context) (*exec.Cmd, error) {
			return c.DirectTmuxAttachCommand(ctx, "agentdeck_x_1", "")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cmd, err := tc.make(ctx)
			if err != nil {
				t.Fatalf("造命令不该失败：%v", err)
			}
			// 调用方的 defer cancel()：解析结束，deadline 该释放了。
			cancel()

			// 标准库语义：CommandContext 会设 Cancel，Command 不会。
			// 命令一旦被 context 绑住，Cancel 就非空，Start 时会直接报 context canceled。
			if cmd.Cancel != nil {
				t.Error("交互式命令被 context 绑住了：解析的 deadline 会把会话一起掐掉")
			}

			// 行为上也验一遍，不只看字段。
			// 这里换成一条一定跑得起来的命令，只保留 context 绑定这一个变量。
			probe := *cmd
			probe.Path = truePath
			probe.Args = []string{truePath}
			if err := probe.Start(); err != nil {
				if errors.Is(err, context.Canceled) {
					t.Fatal("命令启动时就是 context canceled：解析用的 deadline 绑错了地方")
				}
				t.Fatalf("启动失败：%v", err)
			}
			_ = probe.Wait()
		})
	}
}
