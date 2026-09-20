package agentdeck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// profile 留空会落到上游的默认 profile，那可能是使用者的真实工作数据。
// 上游自己记过一次事故：测试跑在真实 profile 上，一次覆盖掉 36 个生产会话。
func TestClientRefusesEmptyProfile(t *testing.T) {
	if _, err := NewClient("/bin/true", "", "127.0.0.1:1", "tok"); err == nil {
		t.Fatal("profile 留空时必须拒绝构造客户端")
	}
	if _, err := NewClient("", "_test", "127.0.0.1:1", "tok"); err == nil {
		t.Fatal("没有可执行文件路径时必须拒绝构造客户端")
	}
	if _, err := NewClient("/bin/true", "_test", "127.0.0.1:1", "tok"); err != nil {
		t.Fatalf("参数齐全时不该报错：%v", err)
	}
}

func TestOnlyListedSubcommandsMayRun(t *testing.T) {
	c, err := NewClient("/bin/true", "_test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 这些都是上游有、但 Work Deck 本轮不该碰的子命令。
	for _, sub := range []string{"remove", "rm", "session", "fleet", "mcp", "conductor", "remote", "add"} {
		_, err := c.exec(context.Background(), sub)
		var refused *ErrSubcommandNotAllowed
		if !errors.As(err, &refused) {
			t.Errorf("子命令 %q 应当被拒绝，实际得到 %v", sub, err)
		}
	}
}

// profile 必须真的传进子进程环境，否则隔离是空话。
func TestProfileIsForcedIntoChildEnv(t *testing.T) {
	t.Setenv("AGENTDECK_PROFILE", "someone-elses-real-profile")
	c, err := NewClient("/bin/true", "_n036fixture", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, kv := range c.env() {
		if strings.HasPrefix(kv, "AGENTDECK_PROFILE=") {
			seen = append(seen, kv)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("子进程环境里应当恰好有一个 AGENTDECK_PROFILE，实际 %v", seen)
	}
	if seen[0] != "AGENTDECK_PROFILE=_n036fixture" {
		t.Errorf("profile 没被顶掉：%s", seen[0])
	}
}

// 派工说不清模型就不许派。
func TestLaunchRequestRefusesUnknownModel(t *testing.T) {
	base := LaunchRequest{ProjectPath: "/tmp/x", Tool: "claude", Model: "m"}
	if err := base.Validate(); err != nil {
		t.Fatalf("齐全的请求不该报错：%v", err)
	}
	noModel := base
	noModel.Model = ""
	if err := noModel.Validate(); err == nil {
		t.Error("模型为空时必须拒绝派工")
	}
	noTool := base
	noTool.Tool = ""
	if err := noTool.Validate(); err == nil {
		t.Error("工具为空时必须拒绝派工")
	}
	noPath := base
	noPath.ProjectPath = ""
	if err := noPath.Validate(); err == nil {
		t.Error("项目路径为空时必须拒绝派工")
	}
}

// 依赖清单不能写完就烂掉：清单里列的字段必须真的是结构体在读的字段。
func TestManifestMatchesTheStructsWeActuallyDecode(t *testing.T) {
	check := func(name string, listed []string, v any) {
		t.Helper()
		actual := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			actual[strings.Split(tag, ",")[0]] = true
		}
		for _, f := range listed {
			if !actual[f] {
				t.Errorf("%s：清单列了字段 %q，但代码并不解析它", name, f)
			}
		}
		for f := range actual {
			var found bool
			for _, l := range listed {
				if l == f {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s：代码解析了字段 %q，但依赖清单没列", name, f)
			}
		}
	}
	check("launch --json", LaunchFields, LaunchResult{})
	check("会话对象", SessionFields, Session{})
	check("菜单事件", MenuFields, MenuEvent{})
}

// 清单里列的子命令，必须正好是白名单。
func TestManifestSubcommandsMatchAllowList(t *testing.T) {
	if !reflect.DeepEqual(Subcommands, []string{"launch", "web", "session attach", "session output"}) {
		t.Errorf("白名单变了：%v。依赖清单与 MANIFEST.md 也要同步更新", Subcommands)
	}
	src := readPackageSource(t)
	for _, ep := range HTTPEndpoints {
		if !strings.Contains(src, ep) {
			t.Errorf("清单列了端点 %q，但包里没有任何代码请求它", ep)
		}
	}
}

// 人读清单与机器清单不能各说各话。
func TestHumanReadableManifestStaysInSync(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(packageDir(t), "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	for _, must := range append(append([]string{}, HTTPEndpoints...), UpstreamVersion, UpstreamCommit) {
		if !strings.Contains(md, must) {
			t.Errorf("MANIFEST.md 里没提到 %q", must)
		}
	}
	for _, sub := range Subcommands {
		if !strings.Contains(md, "`"+sub+"`") {
			t.Errorf("MANIFEST.md 里没提到子命令 %q", sub)
		}
	}
	for _, env := range EnvVars {
		if !strings.Contains(md, env) {
			t.Errorf("MANIFEST.md 里没提到环境变量 %q", env)
		}
	}
}

// 本轮起可以接管终端，但仍然不许替人发按键、不许杀会话。
//
// n036 时这条测试禁止一切 attach；n037 的任务就是进入会话，所以约束改了。
// 改的是「允许进入」这一条，其余一条没松：进去之后是人在操作真实 CLI，
// Work Deck 不替他敲任何键，也没有任何停掉或删掉会话的路径。
func TestAdapterMayAttachButNeverSendsKeysOrKills(t *testing.T) {
	src := readPackageSource(t)
	for _, forbidden := range []string{"send-keys", "kill-session", "kill-server", "respawn-pane", "\"kill\""} {
		if strings.Contains(src, forbidden) {
			t.Errorf("包里出现了 %q：进去之后是人在操作，Work Deck 不替他动手", forbidden)
		}
	}
	// 进入用的是上游自己的 session attach，或默认 server 上的 tmux attach-session。
	if !strings.Contains(src, "attach-session") {
		t.Error("本轮应当具备进入会话的能力")
	}
}

func packageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("拿不到测试文件路径")
	}
	return filepath.Dir(file)
}

func readPackageSource(t *testing.T) string {
	t.Helper()
	dir := packageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

// session 底下有一堆会毁掉别人工作的兄弟命令，白名单必须精确到第二级。
//
// 只写 "session" 就放行，等于顺手给了 Work Deck 杀会话、删注册表的能力，
// 而它没有任何理由需要这些。
func TestDangerousSessionSubcommandsAreRefused(t *testing.T) {
	c, err := NewClient("/bin/true", "_test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dangerous := [][]string{
		{"session", "stop", "x"},
		{"session", "remove", "x"},
		{"session", "restart", "x"},
		{"session", "cleanup"},
		{"session", "archive", "x"},
		{"session", "fork", "x"},
		{"session", "set", "x", "title", "y"},
		{"session", "revive"},
	}
	for _, args := range dangerous {
		_, err := c.exec(context.Background(), args[0], args[1:]...)
		var refused *ErrSubcommandNotAllowed
		if !errors.As(err, &refused) {
			t.Errorf("%v 必须被拒绝，实际得到 %v", args, err)
		}
	}
}

// 允许的那两条要真的能过白名单这一关。
func TestAllowedSessionSubcommandsPassTheGate(t *testing.T) {
	if !isAllowedSubcommand("session", "attach") {
		t.Error("session attach 应当被允许")
	}
	if !isAllowedSubcommand("session", "output") {
		t.Error("session output 应当被允许")
	}
	if isAllowedSubcommand("session") {
		t.Error("光一个 session 不该被允许：那会把 stop/remove 一起放行")
	}
	if isAllowedSubcommand("session", "stop") {
		t.Error("session stop 不该被允许")
	}
}

// 预览是只读的：命令里不许出现任何会改动会话的动作。
func TestPreviewPathIsReadOnly(t *testing.T) {
	src := readPackageSource(t)
	for _, forbidden := range []string{"send-keys", "kill-session", "kill-server", "respawn"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("包里出现了 %q，预览与进入都不该改动会话状态", forbidden)
		}
	}
}

func TestStripANSILeavesReadableText(t *testing.T) {
	in := "\x1b[32m绿字\x1b[0m 普通\x1b[1;31m红粗\x1b[m 结束"
	got := stripANSI(in)
	want := "绿字 普通红粗 结束"
	if got != want {
		t.Errorf("去转义结果是 %q，期望 %q", got, want)
	}
}

func randHex(t *testing.T) string {
	t.Helper()
	return randomHex(t, 4)
}

// socket 名要真的传进 tmux 的参数里。
//
// 这个字段在上游带 omitempty：默认 server 时不出现，非默认时才有。
// 本包此前只看了一个默认 socket 的会话就断言「接口不暴露它」，那是假阴性。
func TestDirectAttachPassesSocketNameAsArgv(t *testing.T) {
	c, err := NewClient("/bin/true", "_test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 非默认 socket：必须带 -L。
	cmd, err := c.DirectTmuxAttachCommand(context.Background(), "agentdeck_x_1", "mysock")
	if err != nil {
		t.Fatalf("有 socket 名时不该拒绝：%v", err)
	}
	want := []string{"tmux", "-L", "mysock", "attach-session", "-t", "agentdeck_x_1"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("参数是 %v，期望 %v", cmd.Args, want)
	}

	// 默认 server：socket 名为空是正常的，不带 -L。
	cmd, err = c.DirectTmuxAttachCommand(context.Background(), "agentdeck_y_2", "")
	if err != nil {
		t.Fatalf("默认 socket 时不该拒绝：%v", err)
	}
	want = []string{"tmux", "attach-session", "-t", "agentdeck_y_2"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("参数是 %v，期望 %v", cmd.Args, want)
	}

	// 没有会话名才是真的没法直连。
	if _, err := c.DirectTmuxAttachCommand(context.Background(), "", "sock"); err == nil {
		t.Error("没有 tmux 会话名时必须拒绝")
	}
}

// 菜单里要能取出 socket 名，取不到和为空是两回事。
func TestTmuxTargetOfDistinguishesEmptySocketFromMissingSession(t *testing.T) {
	ev := MenuEvent{Items: []MenuItem{
		{Type: "session", Session: &Session{ID: "a", TmuxSession: "t_a", TmuxSocketName: "s1"}},
		{Type: "session", Session: &Session{ID: "b", TmuxSession: "t_b"}},
	}}
	if sess, sock, ok := ev.TmuxTargetOf("a"); !ok || sess != "t_a" || sock != "s1" {
		t.Errorf("取 a 得到 %q %q %v", sess, sock, ok)
	}
	// socket 为空表示默认 server，仍然是「取到了」。
	if sess, sock, ok := ev.TmuxTargetOf("b"); !ok || sess != "t_b" || sock != "" {
		t.Errorf("取 b 得到 %q %q %v", sess, sock, ok)
	}
	if _, _, ok := ev.TmuxTargetOf("不存在"); ok {
		t.Error("不存在的会话不该报告取到了")
	}
}

// 依赖清单必须列出 socket 名字段，否则下次又会有人以为它不存在。
func TestManifestListsSocketName(t *testing.T) {
	var listed bool
	for _, f := range SessionFields {
		if f == "tmuxSocketName" {
			listed = true
		}
	}
	if !listed {
		t.Error("依赖清单没列 tmuxSocketName")
	}
	data, err := os.ReadFile(filepath.Join(packageDir(t), "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	if !strings.Contains(md, "tmuxSocketName") {
		t.Error("MANIFEST.md 没提 tmuxSocketName")
	}
	if !strings.Contains(md, "omitempty") {
		t.Error("MANIFEST.md 没说明该字段为什么会看不见")
	}
}
