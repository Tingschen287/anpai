package wirecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// 返工 4：外部原文进入码参数的入口清单，由守卫测试盯着。
//
// 打码的真标记只由 deckstore.Clean 写；外部来的文字如果带同样的私用区字符，必须在进来的那一刻换掉（i18n.Neutralize）。
// 「进来」指：起外部命令、读文件、读 HTTP 响应或请求体。凡是生产代码里调用了这些的文件，都必须出现在下面的表里，
// 要么写明「已中和」（文件里必须真有 Neutralize 调用），要么写明不中和的理由。新增入口不登记，这条测试就失败。
// 反过来，登记了但文件里已经没有入口，也失败（清单不许过期）。
const (
	neutral = "已中和"
	exempt  = "豁免"
)

type intake struct{ kind, why string }

var intakeFiles = map[string]intake{
	// ---- 已中和：外部原文 ----
	"internal/deckd/server.go":         {neutral, "readBody 解码后 neutralizeValue；readBodyRaw 只给两处：带指纹的开子 agent 请求（planSpawn 里先中和业务副本 b，原样的 raw 只用来核对指纹，不进错误参数或通知）、owner 回答决定（option 只和存下来的选项比较；比较失败时 Store.Answer 在 option 进错误参数前先中和）"},
	"internal/deckd/marks.go":          {neutral, "markFilter 先 neutralRequest 处理路径、查询串、Last-Event-ID；请求体只交给测试钩子，不进业务"},
	"internal/agentdeck/client.go":     {neutral, "上游 agent-deck 子命令的 stdout、stderr 和 HTTP 响应"},
	"internal/agentdeck/events.go":     {neutral, "上游事件流的每一行"},
	"internal/agentdeck/deckmux.go":    {neutral, "tmux 命令的输出（含窗格画面）和 stderr"},
	"internal/agentdeck/socketscan.go": {neutral, "tmux 的 stderr 与实例号"},
	"internal/agentdeck/panes.go":      {neutral, "tmux list-panes 的输出"},
	"internal/agentdeck/toolinfo.go":   {neutral, "CLI 的 --version 与列模型输出"},
	"internal/agentdeck/argallow.go":   {neutral, "/proc 里进程的命令行"},
	"internal/agentdeck/inbox.go":      {neutral, "/proc 里进程的命令行"},
	"internal/agentdeck/bypasscfg.go":  {neutral, "上游 agent-deck 的配置文件"},
	"internal/projectctl/client.go":    {neutral, "projectctl 子进程的 stdout、stderr"},
	"internal/projectctl/graph.go":     {neutral, "项目的 work-graph.yaml"},
	"internal/projectctl/snapshot.go":  {neutral, "快照缓存文件（内容来自 projectctl 输出，projectctl 不写真标记）"},
	"internal/projectctl/status.go":    {neutral, "项目的 PROJECT-STATUS.md"},
	"internal/dashquota/dashquota.go":  {neutral, "Token 看板的 HTTP 响应和额度缓存文件"},
	// ---- 豁免：不是外部原文，或读的是 deck 自己写的、可能含真标记的文件 ----
	"internal/deckstore/files.go":       {exempt, "读 deck 自己写的状态文件（会话、决定、通知），里面有真标记，不能中和"},
	"internal/deckstore/store.go":       {exempt, "读 deck 自己写的注册表、通知，里面有真标记，不能中和"},
	"internal/deckstore/settings.go":    {exempt, "owner 自己编辑的设置 YAML；deck 写回时也可能带真标记；只有 owner 能写"},
	"internal/deckstore/panels.go":      {exempt, "owner 自己编辑的面板注册表 YAML，没有自由文字进参数"},
	"internal/deckclient/client.go":     {exempt, "读 deckd 的响应：deckd 是可信的生产者，响应里有真标记，必须保留给界面翻译"},
	"internal/lease/lease.go":           {exempt, "读 deck 自己写的接管记录，内容来自本进程的常量和会话号"},
	"internal/writerlock/writerlock.go": {exempt, "读 deck 自己写的锁文件，只有进程号和时间"},
	"internal/identity/identity.go":     {exempt, "读 /proc 里的数字字段（进程号、起始时间），没有文字"},
	"internal/agentdeck/procbypass.go":  {exempt, "读 /proc/<pid>/stat 里的数字字段，没有文字"},
	"internal/agentdeck/clients.go":     {exempt, "tmux list-clients 的输出只数行数，输出文字不进任何参数"},
	"internal/agentdeck/attach.go":      {exempt, "只构造 tmux attach 命令，不读它的输出"},
	"internal/binding/store.go":         {exempt, "读 deck 自己写的节点绑定文件，只有节点号和会话号"},
	"internal/config/config.go":         {exempt, "owner 自己写的启动配置，只有路径和档位表"},
	"internal/logo/logo.go":             {exempt, "读 deck 自己写的画法文件，只有 sixel/blocks/none"},
	"internal/queue/store.go":           {exempt, "读 deck 自己写的队列文件，旧界面 internal/ui 用，不经 deckd"},
	"internal/deckui/newproject.go":     {exempt, "新建项目时列目录名，只经 sanitizeFull 显示，不进错误码参数；选定的路径随请求体发给 deckd，在 readBody 中和"},
	"internal/ui/verdict.go":            {exempt, "旧界面 internal/ui 读评审报告，不经 deckd，不进错误码参数"},
	"cmd/deck/agent.go":                 {exempt, "命令行读 stdin 或文件，原样发给 deckd；deckd 的 readBody 是入口，在那里中和"},
}

// neutralCall 认调用，不认注释里的字。
// 段 3 起，解码外部 JSON 的文件用 i18n.UnmarshalExternal、DecodeExternal（解码后再中和），也算中和调用；
// 解码后的中和是否真在解码的函数里，由 decode_test.go 的结构守卫另外盯着。
var neutralCall = regexp.MustCompile(`(i18n\.(Neutralize(Bytes|JSON|Value)?|UnmarshalExternal|DecodeExternal)|neutralizeValue|neutralRequest)\(`)

func workRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// 入口的判定：调用 exec.Command / exec.CommandContext、os.ReadFile、os.Open、os.ReadDir、io.ReadAll、
// http.NewRequest / http.NewRequestWithContext、bufio.NewScanner（读 reader）、net.Dial / DialContext。
var intakeCalls = map[string]bool{
	"exec.Command": true, "exec.CommandContext": true,
	"os.ReadFile": true, "os.Open": true, "os.ReadDir": true,
	"io.ReadAll":      true,
	"http.NewRequest": true, "http.NewRequestWithContext": true,
	"net.Dial": true,
}

func intakeFilesIn(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/wirecheck/") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", p, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			name := id.Name + "." + sel.Sel.Name
			if intakeCalls[name] || (id.Name == "net" && sel.Sel.Name == "Dialer") {
				found[rel] = append(found[rel], name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestN060ExternalIntakeFilesAreListed(t *testing.T) {
	root := workRoot(t)
	found := intakeFilesIn(t, root)
	var names []string
	for f := range found {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		e, ok := intakeFiles[f]
		if !ok {
			t.Errorf("%s 调用了 %v，是外部原文的入口，没有登记：在 intakeFiles 里写明已中和或豁免的理由", f, found[f])
			continue
		}
		if e.why == "" {
			t.Errorf("%s 的登记没有写理由", f)
		}
		if e.kind != neutral {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if !neutralCall.Match(src) {
			t.Errorf("%s 登记为已中和，但文件里没有 Neutralize 调用", f)
		}
	}
	for f := range intakeFiles {
		if _, ok := found[f]; !ok {
			t.Errorf("%s 登记了，但文件里已经没有入口调用（或文件没了）：清单过期", f)
		}
	}
}
