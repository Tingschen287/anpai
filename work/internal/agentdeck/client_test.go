package agentdeck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
// 改的是「允许进入」这一条，其余一条没松：进去之后是人在操作真实 CLI，
// Work Deck 不替他敲任何键，也没有任何停掉或删掉会话的路径。
func TestAdapterMayAttachButNeverSendsKeysOrKills(t *testing.T) {
	forbidden := append([]string{"kill-session", "kill-server", "respawn-pane", "\"kill\""},
		keyInjectionCommands...)
	for _, bad := range forbidden {
		for _, line := range linesOfPackageSource(t) {
			// keyInjectionCommands 那一行是禁令本身，放过它；
			// 证明「不做某件事」总得先说得出那件事叫什么。
			if strings.Contains(line, "keyInjectionCommands = ") {
				continue
			}
			if strings.Contains(line, bad) {
				t.Errorf("包里出现了 %q：进去之后是人在操作，Work Deck 不替他动手（%s）",
					bad, strings.TrimSpace(line))
			}
		}
	}
	// 进入用的是上游自己的 session attach，或默认 server 上的 tmux attach-session。
	if !strings.Contains(readPackageSource(t), "attach-session") {
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
		all := allAnchorsOf(e)
		for i, c := range e.Claims {
			label := fmt.Sprintf("%s 的第 %d 条断言", sub, i+1)
			checkClaim(t, src, label, c)
			checkEvidenceIsNotTheSharedOne(t, label, c.Evidence)
			checkMeansDoesNotSneakInUncitedClaims(t, label, c, all)
		}
	}
	shared := allAnchorsOf(SharedStorageInit)
	for i, c := range SharedStorageInit.Claims {
		label := fmt.Sprintf("SharedStorageInit 的第 %d 条断言", i+1)
		checkClaim(t, src, label, c)
		checkMeansDoesNotSneakInUncitedClaims(t, label, c, shared)
	}
}

// allAnchorsOf 把一条命令所有锚点与被禁符号拼起来，供 Means 核对用。
func allAnchorsOf(e SideEffect) string {
	var b strings.Builder
	for _, c := range e.Claims {
		for _, ev := range c.Evidence {
			b.WriteString(ev.Anchor)
			b.WriteByte('\n')
		}
		if c.Absence != nil {
			for _, a := range c.Absence.Absent {
				b.WriteString(a)
				b.WriteByte('\n')
			}
			for _, ref := range c.Absence.In {
				b.WriteString(ref.Func)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
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

// checkClaim 核对一条断言。正面与否定各走各的路，但都必须有路可走。
func checkClaim(t *testing.T, src, label string, c Claim) {
	t.Helper()
	if strings.TrimSpace(c.Does) == "" {
		t.Errorf("%s 没说上游做了什么", label)
		return
	}
	hasEvidence, hasAbsence := len(c.Evidence) > 0, c.Absence != nil
	switch {
	case hasEvidence && hasAbsence:
		t.Errorf("%s 同时给了正面依据和缺席依据，说不清它到底在证明什么", label)
		return
	case !hasEvidence && !hasAbsence:
		t.Errorf("%s 一份依据都没有：「%s」这句话的出处在哪", label, c.Means)
		return
	case hasAbsence:
		checkAbsence(t, src, label, c)
		return
	}
	checkEvidence(t, src, label, c.Evidence)
	checkDoesIsLiterallyInItsOwnAnchor(t, label, c)
}

// checkEvidence 验正面依据：锚点在指定行上，且那一行在 Within 说的函数里。
func checkEvidence(t *testing.T, src, label string, where []Evidence) {
	t.Helper()
	for _, e := range where {
		if e.Path == "" || e.Line < 1 || e.Within == "" {
			t.Errorf("%s 的依据 %q 不完整：路径、行号、所属函数三者缺一不可", label, e)
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
		// 同一段调用在一个文件里常常出现好几次，分属不同执行路径。
		if fn := enclosingFunc(lines, e.Line); !strings.Contains(fn, e.Within) {
			t.Errorf("%s 的依据 %q 不在 %s 里，那一行属于：%s",
				label, e, e.Within, strings.TrimSpace(fn))
		}
	}
}

// checkAbsence 验否定依据：那几个函数体里确实搜不到那些东西。
func checkAbsence(t *testing.T, src, label string, c Claim) {
	t.Helper()
	a := c.Absence
	// 边界说不清楚的否定断言等于没有依据：函数体级的搜索不覆盖它调用的东西。
	if strings.TrimSpace(a.Scope) == "" {
		t.Errorf("%s 是否定断言却没写 Scope：说不清这份证明覆盖到哪，就不算证明", label)
		return
	}
	if len(a.In) == 0 || len(a.Absent) == 0 {
		t.Errorf("%s 的缺席依据不完整：要搜哪些函数、搜什么，都得写", label)
		return
	}
	// 否定断言说「不做 X」，那 X 就得真的在被搜的清单里，
	// 否则这条断言和这次搜索各说各话。
	var covered bool
	for _, forbidden := range a.Absent {
		if forbidden == c.Does {
			covered = true
			break
		}
	}
	if !covered {
		t.Errorf("%s 说不做 %q，但搜的是 %v——断言与搜索对不上", label, c.Does, a.Absent)
	}
	for _, ref := range a.In {
		lines, err := upstreamLines(src, ref.Path)
		if err != nil {
			t.Errorf("%s 的缺席依据指向 %s，但读不到：%v", label, ref.Path, err)
			continue
		}
		body, ok := funcBody(lines, ref.Func)
		if !ok {
			t.Errorf("%s 的缺席依据说要搜 %s，但上游里找不到这个函数", label, ref)
			continue
		}
		for _, forbidden := range a.Absent {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s 说「%s」，但 %s 里出现了 %q", label, c.Means, ref, forbidden)
			}
		}
	}
}

// checkDoesIsLiterallyInItsOwnAnchor 验断言的那个符号真的在它自己的锚点上。
//
// **逐字包含，不是「共享某个 token」。** 这一条是整套清单里唯一
// 机器能给出硬保证的地方：Does 必须是上游代码里真实存在的符号，
// 所以它编不出假话。
//
// 「自己的」也是重点：attempt 11 把一条命令所有锚点拼成一个字符串去匹配，
// 于是只要有一句话对得上，其余的都搭便车过关。
func checkDoesIsLiterallyInItsOwnAnchor(t *testing.T, label string, c Claim) {
	t.Helper()
	for _, ev := range c.Evidence {
		if strings.Contains(ev.Anchor, c.Does) {
			return
		}
	}
	var anchors []string
	for _, ev := range c.Evidence {
		anchors = append(anchors, ev.Anchor)
	}
	t.Errorf("%s 说上游做的是 %q，但它自己的锚点里根本没有这个符号：%q",
		label, c.Does, strings.Join(anchors, " / "))
}

// checkMeansDoesNotSneakInUncitedClaims 防止解释偷偷变成断言。
//
// Means 是给人看的，测试不判断它的中文说得对不对——**机器做不到这件事，
// 假装做得到才是问题**。但如果有人在 Means 里点了一个技术符号的名，
// 那就不再是解释而是断言了，得有依据兜着。
//
// 所以规矩是：Means 里出现的标识符，要么在本条的锚点上，
// 要么在同一条命令别处的锚点上（同一件事拆成几条 Claim 是常事），
// 否则就是一句没有出处的技术断言。
func checkMeansDoesNotSneakInUncitedClaims(t *testing.T, label string, c Claim, allAnchors string) {
	t.Helper()
	lower := strings.ToLower(allAnchors)
	for _, tok := range identifier.FindAllString(c.Means, -1) {
		if !strings.Contains(lower, strings.ToLower(tok)) {
			t.Errorf("%s 的解释里点名了 %q，但这条命令的依据里没有它——"+
				"点到具体符号就不再是解释，是断言，得有出处", label, tok)
		}
	}
}

func upstreamLines(src, path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(src, path))
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

// enclosingFunc 从某一行往上找最近的函数声明。
func enclosingFunc(lines []string, line int) string {
	for i := line - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "func ") {
			return lines[i]
		}
	}
	return "(文件顶层，不在任何函数里)"
}

// funcBody 抽出一个函数从声明行到下一个顶层声明之间的文本。
//
// 按文本切，不做语法分析：够用，且看得懂。抽不到就说抽不到，不猜。
func funcBody(lines []string, name string) (string, bool) {
	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "func ") {
			continue
		}
		// 名字后面必须直接跟左括号或泛型括号，免得 Foo 匹配到 FooBar。
		if idx := strings.Index(line, name); idx >= 0 {
			rest := line[idx+len(name):]
			if strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "[") {
				start = i
				break
			}
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "func ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
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

// identifier 抓断言与锚点里的技术记号：函数名、字段名、带连字符的子命令。
var identifier = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_.\-]{7,}`)
