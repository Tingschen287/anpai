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
	if !reflect.DeepEqual(Subcommands, []string{"launch", "web"}) {
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

// 本轮不 attach、不发按键，包里不该有这类东西。
func TestAdapterHasNoAttachOrSendCapability(t *testing.T) {
	src := readPackageSource(t)
	for _, forbidden := range []string{"attach-session", "send-keys", "tmux attach", "\"kill-session\""} {
		if strings.Contains(src, forbidden) {
			t.Errorf("包里出现了 %q，本轮不接管终端，那是后续节点的事", forbidden)
		}
	}
}

// 「不知道模型」和「这工具没有模型」必须分得清清楚楚。
func TestModelNotApplicableIsNotABackDoor(t *testing.T) {
	// 声明了不适用，可以不给模型。
	ok := LaunchRequest{ProjectPath: "/tmp/x", Tool: "shell", ModelNotApplicable: true}
	if err := ok.Validate(); err != nil {
		t.Errorf("显式声明工具没有模型时不该报错：%v", err)
	}
	// 没声明又不给模型，照样拒绝——这才是「不知道」。
	unknown := LaunchRequest{ProjectPath: "/tmp/x", Tool: "claude"}
	if err := unknown.Validate(); err == nil {
		t.Error("没声明不适用又不给模型，必须拒绝")
	}
	// 自相矛盾的请求也拒绝。
	both := LaunchRequest{ProjectPath: "/tmp/x", Tool: "claude", Model: "m", ModelNotApplicable: true}
	if err := both.Validate(); err == nil {
		t.Error("既说没有模型又给了模型，必须拒绝")
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
