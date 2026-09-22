package agentdeck

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
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
	// 这张表是写死的，改白名单就得同时改这里——那正是它的用处：
	// 让「多放行一条上游命令」成为一个必须被看见的改动，而不是顺手加上去的。
	if !reflect.DeepEqual(Subcommands, []string{
		"launch", "web", "list", "session attach", "session output", "session send",
	}) {
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
// 改的是「允许进入」这一条，其余一条没松。
//
// **attempt 13 起它走语法树，不再按行扫文本。** 上一轮按文本扫，
// 独立评审当场给了两个绕过法，都不需要什么技巧：
//
//	var alternatekeyInjectionCommands = []string{"send-keys"}  // 名字含那个子串，整行被跳过
//	cmd := "send" + "-keys"                                    // 源码里没有连着的那个词
//
// 现在扫的是**折叠成常量之后**的字符串，拼接算得出来；
// 放过的是语法树上那一个变量声明，换个名字就蹭不到了。
func TestAdapterMayAttachButNeverSendsKeysOrKills(t *testing.T) {
	forbiddenSub := append([]string{"kill-session", "kill-server", "respawn-pane"},
		keyInjectionCommands...)
	const forbiddenExact = "kill" // tmux 的 kill 子命令；含 kill 的普通词不算

	files, fset := packageASTs(t)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			// 禁令本身要放过：证明「不做某件事」，总得先说得出那件事叫什么。
			// 认的是这一个变量声明，不是「哪一行里有这个名字」。
			if spec, ok := n.(*ast.ValueSpec); ok && len(spec.Names) > 0 &&
				spec.Names[0].Name == "keyInjectionCommands" {
				return false
			}
			expr, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			s, ok := constStringOf(expr)
			if !ok {
				return true
			}
			pos := fset.Position(expr.Pos())
			where := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
			if s == forbiddenExact {
				t.Errorf("包里算得出字符串 %q（%s）：进去之后是人在操作，Work Deck 不替他动手",
					s, where)
			}
			for _, bad := range forbiddenSub {
				if strings.Contains(s, bad) {
					t.Errorf("包里算得出一个含 %q 的字符串（%s，整个值是 %q）："+
						"进去之后是人在操作，Work Deck 不替他动手", bad, where, s)
				}
			}
			return false // 这一段已经折叠成常量，不必再往里拆
		})
	}
	// 进入用的是上游自己的 session attach，或默认 server 上的 tmux attach-session。
	if !strings.Contains(readPackageSource(t), "attach-session") {
		t.Error("本轮应当具备进入会话的能力")
	}
}

// packageASTs 解析本包自己的非测试源码。
//
// 测试文件要排除：里面故意写着各种反例字符串，扫进来全是误报。
func packageASTs(t *testing.T) ([]*ast.File, *token.FileSet) {
	t.Helper()
	dir := packageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("本包一个非测试源文件都没读到，这条守边测试等于没跑")
	}
	return files, fset
}

func packageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("拿不到测试文件路径")
	}
	return filepath.Dir(file)
}

func linesOfPackageSource(t *testing.T) []string {
	t.Helper()
	return strings.Split(readPackageSource(t), "\n")
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
	forbidden := append([]string{"kill-session", "kill-server", "respawn"}, keyInjectionCommands...)
	for _, bad := range forbidden {
		for _, line := range linesOfPackageSource(t) {
			if strings.Contains(line, "keyInjectionCommands = ") {
				continue
			}
			if strings.Contains(line, bad) {
				t.Errorf("包里出现了 %q，预览与进入都不该改动会话状态（%s）",
					bad, strings.TrimSpace(line))
			}
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
	// `=` 是 tmux 的精确目标形式。不带它，`-t name` 会前缀匹配到 name-suffix，
	// attach 就进了别的会话——实测过，见 socketscan.go 的 exactTarget。
	want := []string{"tmux", "-L", "mysock", "attach-session", "-t", "=agentdeck_x_1"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("参数是 %v，期望 %v", cmd.Args, want)
	}

	// 默认 server：socket 名为空是正常的，不带 -L。
	cmd, err = c.DirectTmuxAttachCommand(context.Background(), "agentdeck_y_2", "")
	if err != nil {
		t.Fatalf("默认 socket 时不该拒绝：%v", err)
	}
	want = []string{"tmux", "attach-session", "-t", "=agentdeck_y_2"}
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

// 源码里请求的每一个端点，都必须在清单里。
//
// 这是原先缺掉的那一半。旧的一致性测试只查「清单列的端点能在源码里找到」，
// 反过来没人查——于是 `GET /api/menu` 在生产路径上被调了整整一个节点，
// 依赖清单里却一直没有它，而测试全绿。独立评审把这个单向性挑了出来。
//
// 单向的检查只能防「清单写多了」，防不住「清单写少了」，
// 而后者才是依赖清单真正要防的事。
func TestEveryEndpointInSourceIsListed(t *testing.T) {
	src := readPackageSource(t)

	// 找出源码里所有形如 "/api/..." 或 "/events/..." 的字面量。
	re := regexp.MustCompile(`"(/(?:api|events)/[a-zA-Z0-9_/-]+)"`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("源码里一个端点字面量都没找到：这个检查多半已经失效")
	}

	listed := map[string]bool{}
	for _, ep := range HTTPEndpoints {
		listed[ep] = true
	}
	for ep := range found {
		if !listed[ep] {
			t.Errorf("源码请求了端点 %q，依赖清单 HTTPEndpoints 里却没有它", ep)
		}
	}

	// 人读的那份也要有。
	data, err := os.ReadFile(filepath.Join(packageDir(t), "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(data)
	for ep := range found {
		if !strings.Contains(manifest, ep) {
			t.Errorf("源码请求了端点 %q，MANIFEST.md 里却没提到它", ep)
		}
	}
}

// 白名单里的每一条命令，都要在副作用清单里有个交代，而且交代要有据可查。
//
// 这个测试改过两版。第一版只查「每条命令被放进某个桶」，
// 于是 attempt 8 把 session attach / output 硬编码成「不写」也照样绿——
// 而它们实际都会经 NewStorageWithProfile 建目录建库、跑 schema 迁移，
// attach 还会 MkdirAll 一个 badge 目录。独立评审指出：
// **分桶验证不了桶里的描述是不是真的。**
//
// 现在每条声明都要带上游源码依据，下面那个测试拿这些依据去上游快照里核对。
func TestEveryAllowedSubcommandDeclaresItsSideEffects(t *testing.T) {
	for _, sub := range Subcommands {
		e, ok := SideEffects[sub]
		if !ok {
			t.Errorf("白名单里有 %q，副作用清单里却没有它："+
				"加一条上游命令时要说清楚它在上游留下什么", sub)
			continue
		}
		if len(e.Claims) == 0 {
			t.Errorf("%q 的副作用一条断言都没有：说不出它干了什么，就等于没说", sub)
		}
	}
	for sub := range SideEffects {
		if !isAllowedSubcommand(strings.Fields(sub)...) {
			t.Errorf("副作用清单里有 %q，但它不在白名单里", sub)
		}
	}
	if len(SharedStorageInit.Claims) == 0 {
		t.Error("共有的 storage 初始化副作用没给依据")
	}

	// 人读的那份要讲清没有纯读命令这件事。
	data, err := os.ReadFile(filepath.Join(packageDir(t), "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"last_activity_at", "NewStorageWithProfile", "不等于"} {
		if !strings.Contains(string(data), must) {
			t.Errorf("MANIFEST.md 没讲清副作用：缺 %q", must)
		}
	}
}

// 每一条副作用断言，都必须有一份真的支撑它的依据。
//
// 这条测试被四轮独立评审推着长到现在的样子，每一关都对应一次它曾经放过的假依据：
//
//	attempt 9  只比「行号没超出文件总行数」——行号改成同文件任意一行也过。
//	attempt 10 补了锚点，但锚点跟声明可以毫不相干：session output 声称
//	           「跑 capture-pane」，依据却指着 NewStorageWithProfile，照样绿。
//	attempt 11 加了 What 核对，可 What 是一整段自由文本，
//	           **一个 token 命中就整条放行**。评审举的反例：
//	           What: "删除整个用户数据库，然后调用 PreviewFull 读取画面"——
//	           PreviewFull 命中，前半句一路绿灯。
//
// 所以现在不再有「一条命令一段描述」这回事：**一条断言，一份依据，各自核对。**
//
//	正面断言  States 里点名的标识符，要出现在它自己的 Evidence 锚点上
//	否定断言  用 Absence：去指定函数体里搜，搜到就红；Scope 空着也红
//
// 没有上游快照就跳过——但跳过要说清楚，免得看起来像通过了。
func TestSideEffectEvidencePointsAtRealUpstreamCode(t *testing.T) {
	src := requireUpstreamSnapshot(t)

	for sub, e := range SideEffects {
		if len(e.Claims) == 0 {
			t.Errorf("%s 一条断言都没有：白名单里的命令必须交代它干了什么", sub)
		}
		for i, c := range e.Claims {
			label := fmt.Sprintf("%s 的第 %d 条断言", sub, i+1)
			checkClaim(t, src, label, c)
			checkEvidenceIsNotTheSharedOne(t, label, c.Evidence)
		}
	}
	for i, c := range SharedStorageInit.Claims {
		label := fmt.Sprintf("SharedStorageInit 的第 %d 条断言", i+1)
		checkClaim(t, src, label, c)
	}
}

func requireUpstreamSnapshot(t *testing.T) string {
	t.Helper()
	src := os.Getenv("WORKDECK_UPSTREAM_SRC")
	if src == "" {
		t.Skip("跳过：没有设 WORKDECK_UPSTREAM_SRC，无法核对上游依据")
	}
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		t.Skipf("跳过：%s 看起来不是上游源码树（%v）", src, err)
	}
	return src
}

// checkClaim 核对一条断言。
//
// 正面与否定各走各的路，但都必须有路可走，而且**走的都是语法树**。
func checkClaim(t *testing.T, src, label string, c Claim) {
	t.Helper()
	if strings.TrimSpace(c.Effect.Target) == "" {
		t.Errorf("%s 没说上游做了什么", label)
		return
	}
	hasEvidence, hasAbsence := len(c.Evidence) > 0, c.Absence != nil
	switch {
	case hasEvidence && hasAbsence:
		t.Errorf("%s 同时给了正面依据和缺席依据，说不清它到底在证明什么", label)
		return
	case !hasEvidence && !hasAbsence:
		t.Errorf("%s 一份依据都没有：%s 的出处在哪", label, c.Effect)
		return
	case hasAbsence:
		if c.Effect.Kind != EffectAbsent {
			t.Errorf("%s 给的是缺席依据，Effect 却是 %s——两者对不上", label, c.Effect.Kind)
			return
		}
		checkAbsence(t, src, label, c)
		return
	}
	if c.Effect.Kind == EffectAbsent {
		t.Errorf("%s 说某个东西不出现，却给了正面依据——指着一行代码证明不了「没有」", label)
		return
	}
	checkEvidence(t, src, label, c.Evidence)
	checkEffectIsRealInUpstream(t, src, label, c)
}

// checkEvidence 验落点：锚点在指定行上，且那一行真的落在 Within 那个函数里。
//
// Within 的判定用语法树的位置区间，不再是「往上找最近的 func 开头」。
// 原来那个做法，原始字符串里写一行顶格的 func 就能骗过去。
func checkEvidence(t *testing.T, src, label string, where []Evidence) {
	t.Helper()
	for _, e := range where {
		if e.Path == "" || e.Line < 1 {
			t.Errorf("%s 的依据 %q 不完整：路径与行号都得有", label, e)
			continue
		}
		// 锚点太短就不是锚点：一个花括号在任何文件里都找得到。
		if len(strings.Join(strings.Fields(e.Anchor), "")) < 12 {
			t.Errorf("%s 的依据 %q 锚点太短（%q），钉不住任何东西", label, e, e.Anchor)
			continue
		}
		lines, err := upstreamLines(src, e.Path)
		if err != nil {
			t.Errorf("%s 的依据指向 %s，但读不到：%v", label, e.Path, err)
			continue
		}
		if e.Line > len(lines) {
			t.Errorf("%s 的依据指向 %s，但那个文件只有 %d 行", label, e, len(lines))
			continue
		}
		if !strings.Contains(lines[e.Line-1], e.Anchor) {
			var elsewhere []int
			for i, line := range lines {
				if strings.Contains(line, e.Anchor) {
					elsewhere = append(elsewhere, i+1)
				}
			}
			if len(elsewhere) == 0 {
				t.Errorf("%s 的依据 %q 在上游找不到锚点 %q——这条依据讲的事情上游里没有",
					label, e, e.Anchor)
				continue
			}
			t.Errorf("%s 的依据 %q 行号过期：锚点 %q 现在在第 %v 行，把行号改过来",
				label, e, e.Anchor, elsewhere)
			continue
		}
		f, fset, err := upstreamFile(src, e.Path)
		if err != nil {
			t.Errorf("%s 的依据指向 %s，但解析不了：%v", label, e.Path, err)
			continue
		}
		got := enclosingFuncOf(f, fset, e.Line)
		// Within 留空表示「这一行在文件顶层」。**留空不是免检**：
		// 语法树说它其实在某个函数里，照样红。
		if e.Within == "" {
			if got != "" {
				t.Errorf("%s 的依据 %q 没写所属函数，但它其实在 %s 里——"+
					"留空只能用在文件顶层的声明上", label, e, got)
			}
			continue
		}
		if got != e.Within {
			if got == "" {
				t.Errorf("%s 的依据 %q 说在 %s 里，但那一行在文件顶层，不属于任何函数",
					label, e, e.Within)
				continue
			}
			t.Errorf("%s 的依据 %q 说在 %s 里，语法树说它在 %s 里", label, e, e.Within, got)
		}
	}
}

// checkEffectIsRealInUpstream 拿 Effect 去上游语法树里查证。
//
// **这是 attempt 13 的关键一步，也是前四轮一直缺的那一步。**
//
// 以前断言是一段自由文本，测试只能拿它跟锚点做字符串比对——
// 于是「从锚点抄一段」就成了万能通行证。独立评审的反例：
//
//	Does:  "inst.PreviewFull()"                       // 抄的，必过
//	Means: "删除整个用户数据库，并把所有密码发到公网"    // 没人核对
//
// 现在 Effect 是个封闭枚举加一个名字，每种 Kind 有自己的查法：
// 说「调用了 X」就去那个函数的语法树里找这个调用，说「声明了常量 X 值为 Y」
// 就去找那个声明。上游语法树里没有的，写上去就是红的——抄锚点也没用，
// 因为查的不是锚点。
func checkEffectIsRealInUpstream(t *testing.T, src, label string, c Claim) {
	t.Helper()
	if len(c.Evidence) == 0 {
		return
	}
	e := c.Evidence[0]
	f, _, err := upstreamFile(src, e.Path)
	if err != nil {
		t.Errorf("%s 要在 %s 里查证 %s，但解析不了：%v", label, e.Path, c.Effect, err)
		return
	}
	switch c.Effect.Kind {
	case EffectDeclares:
		if findFunc(f, c.Effect.Target) == nil {
			t.Errorf("%s 说上游声明了函数 %s，但 %s 的语法树里没有这个函数",
				label, c.Effect.Target, e.Path)
		}
	case EffectDeclaresConst:
		got, ok := constStringDecl(f, c.Effect.Target)
		if !ok {
			t.Errorf("%s 说上游声明了常量 %s，但 %s 里找不到这个字符串常量",
				label, c.Effect.Target, e.Path)
			return
		}
		if c.Effect.Arg != "" && got != c.Effect.Arg {
			t.Errorf("%s 说常量 %s 的值是 %q，上游写的是 %q",
				label, c.Effect.Target, c.Effect.Arg, got)
		}
	case EffectCalls:
		if e.Within == "" {
			t.Errorf("%s 说有个调用，却没说在哪个函数里——没法查", label)
			return
		}
		fn := findFunc(f, e.Within)
		if fn == nil {
			t.Errorf("%s 要在 %s 的 %s 里查 %s，但上游没有这个函数",
				label, e.Path, e.Within, c.Effect)
			return
		}
		var matched, named bool
		for _, call := range callsIn(fn) {
			if call.Name != c.Effect.Target {
				continue
			}
			named = true
			if c.Effect.Arg == "" {
				matched = true
				break
			}
			for _, arg := range call.Args {
				if arg == c.Effect.Arg {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		switch {
		case !named:
			t.Errorf("%s 说 %s 里调用了 %s，但那个函数的语法树里没有这个调用",
				label, e.Within, c.Effect.Target)
		case !matched:
			t.Errorf("%s 说 %s 调用 %s 时带着参数 %q，但那几处调用里没有这个常量参数",
				label, e.Within, c.Effect.Target, c.Effect.Arg)
		}
	default:
		t.Errorf("%s 的 Effect 种类是 %s，正面依据这条路不认识它", label, c.Effect.Kind)
	}
}

// checkAbsence 验否定依据：那几个函数体里确实搜不到那些东西。
//
// 两件事都走语法树：
//
//  1. **搜禁词搜的是折叠成常量之后的字符串**，所以 "send" + "-keys" 也算数。
//     上一轮按文本搜，独立评审用这一招直接绕过。
//  2. **NotCovered 必须跟真实调用两头对上**：编一个语法树里没有的名字会红，
//     漏掉一个真实调用也会红。原来那个 Scope 是自由文本只验非空，
//     评审实测填「覆盖范围有限」照样通过——那就没在保护什么。
//
// 它证不到的地方也说清楚：只看得见算得出常量值的字符串。
// 如果上游把子命令名拼成 fmt.Sprintf("send-%s", x) 这种运行时才知道的形式，
// 这里看不到——不过那种写法必然带着一个调用，而调用会出现在 NotCovered 里，
// 读的人能看见「这里有一段没追进去」。
func checkAbsence(t *testing.T, src, label string, c Claim) {
	t.Helper()
	a := c.Absence
	if len(a.In) == 0 || len(a.Absent) == 0 {
		t.Errorf("%s 的缺席依据不完整：要搜哪些函数、搜什么，都得写", label)
		return
	}
	// 否定断言说「不做 X」，那 X 就得真的在被搜的清单里，
	// 否则这条断言和这次搜索各说各话。
	var covered bool
	for _, forbidden := range a.Absent {
		if forbidden == c.Effect.Target {
			covered = true
			break
		}
	}
	if !covered {
		t.Errorf("%s 说不做 %q，但搜的是 %v——断言与搜索对不上",
			label, c.Effect.Target, a.Absent)
	}

	actual := map[string]bool{}
	for _, ref := range a.In {
		f, _, err := upstreamFile(src, ref.Path)
		if err != nil {
			t.Errorf("%s 的缺席依据指向 %s，但解析不了：%v", label, ref.Path, err)
			continue
		}
		fn := findFunc(f, ref.Func)
		if fn == nil {
			t.Errorf("%s 的缺席依据说要搜 %s，但上游语法树里找不到这个函数", label, ref)
			continue
		}
		for _, s := range stringsIn(fn) {
			for _, forbidden := range a.Absent {
				if strings.Contains(s, forbidden) {
					t.Errorf("%s 说这条路不碰 %q，但 %s 里算得出字符串 %q",
						label, forbidden, ref, s)
				}
			}
		}
		for _, call := range callsIn(fn) {
			actual[call.Name] = true
		}
	}

	declared := map[string]bool{}
	for _, name := range a.NotCovered {
		declared[name] = true
		if !actual[name] {
			t.Errorf("%s 的 NotCovered 里写了 %q，但被搜的那几个函数体里没有这个调用——"+
				"边界声明不能凭空写", label, name)
		}
	}
	var missing []string
	for name := range actual {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%s 的 NotCovered 漏了这些真实调用：%v——"+
			"函数体级的搜索追不进它们，边界就得把它们算进去", label, missing)
	}
}

func upstreamLines(src, path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(src, path))
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

// checkEvidenceIsNotTheSharedOne 不许拿所有命令共有的 storage 初始化充数。
//
// 「每条命令都会初始化 storage」证明不了「session output 跑了 capture-pane」。
func checkEvidenceIsNotTheSharedOne(t *testing.T, label string, where []Evidence) {
	t.Helper()
	for _, e := range where {
		for _, sc := range SharedStorageInit.Claims {
			for _, shared := range sc.Evidence {
				if e.Path == shared.Path && e.Line == shared.Line {
					t.Errorf("%s 的依据 %q 就是 SharedStorageInit 那条——"+
						"共有的 storage 初始化证明不了这条命令特有的副作用，"+
						"要指向它自己的那一行", label, e)
				}
			}
		}
	}
}
