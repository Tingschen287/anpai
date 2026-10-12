package agentdeck

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// 本文件是 Work Deck 对 upstream agent-deck 依赖的机器可读清单：子命令白名单在 client.go，
// 这里列 launch 的参数、HTTP 端点、部分输出字段、环境变量、各命令的副作用与 tmux 约定。
// 它不是全集：session output 的 -pane、session send 的 --message-file、list 的 --json
// 这几个参数，以及 ListedSession、MenuItem、Group 的字段，这里没有机器可读的条目，
// 只写在 MANIFEST.md 里。
//
// 它存在的理由：上游一改，这里就是核对基准。人读版本在同目录的 MANIFEST.md。
//
// 有测试盯着它不漂移，但**各条测试查到哪一步是有边界的**，不是「清单外的东西一律会红」：
//   - 字段：只核 LaunchResult、Session、MenuEvent、InboxRecord、WriterStatus 五个结构体，两个方向都核；
//     ListedSession、MenuItem、Group、SessionList 也解析 JSON，不在核对范围内。
//   - 子命令：白名单与测试里写死的那张表必须完全相等。
//   - 端点：按字面量找源码里的 "/api/..."、"/events/..."；拼出来的路径看不见。
//   - 人读清单：只查端点、上游版本与提交、子命令、环境变量这几类在 MANIFEST.md 出现；
//     LaunchFlags、WebFlags、TmuxConventions 不查。字段只有一个特例：TestManifestListsSocketName
//     查 tmuxSocketName 在 SessionFields 与 MANIFEST.md 里都出现、且文档提到 omitempty（只查词语出现）；
//     其余字段不查。MANIFEST.md 多写了什么也不查。
// 边界总表见 4.eval/results/n037_workbench_attach/boundary-limits.md。

// UpstreamVersion 是本清单核对过的上游版本。
const UpstreamVersion = "Agent Deck v1.15.0"

// UpstreamCommit 是上游快照的提交号。
const UpstreamCommit = "01c011b"

// CLIJSONFields 是 Work Deck 从 list --json 的行里读的字段。
//
// 注意它和 SessionFields 不是一套：命名是 snake_case，而且没有 socket 名。
var CLIJSONFields = []string{"id", "title", "tool", "status", "path", "group", "tmux_session", "profile", "archived"}

// ToolsAcceptingModel 是上游内置、launch 时接受 --model 的工具（n042）。
//
// 出处：上游 internal/session/instance.go:9318 SupportsLaunchModel——claude、gemini、opencode、codex，
// 以及用户在上游配置里声明为与 claude / codex 兼容的自定义工具（Work Deck 看不到上游配置，判断不了后者）。
// 升级上游时要重新核对这张表。
var ToolsAcceptingModel = []string{"claude", "codex", "gemini", "opencode"}

// ToolsWithoutModel 是上游内置、根本没有模型可选的工具：给它传 --model，上游 launch 直接失败（n040 终审断点 3）。
var ToolsWithoutModel = []string{"shell"}

// CheckToolModel 按上游契约核对「这个工具有没有模型」与配置声明是否一致。
// 已知的两类冲突在派工前就拒绝，并说清楚怎么改；别的自定义工具判断不了，交给上游。
func CheckToolModel(tool string, modelNotApplicable bool) error {
	switch {
	case !modelNotApplicable && contains(ToolsWithoutModel, tool):
		return fmt.Errorf("工具 %s 没有模型可选，上游 launch 不接受 --model：在配置的这个模型条目里写 \"model_not_applicable\": true", tool)
	case modelNotApplicable && contains(ToolsAcceptingModel, tool):
		return fmt.Errorf("工具 %s 有模型可选，却声明了 model_not_applicable：那等于让上游替你挑默认模型，不派。去掉这个声明并写明模型 id", tool)
	}
	return nil
}

// ToolsWithoutTaskText 是不是 agent 的上游工具：任务说明送进去，会被当成命令逐行执行（n043 复审新问题 1，
// 夹具上实测满屏 command not found；说明里若有能执行的行，就真的执行了）。
//
// 出处：上游对认不出的命令一律按 shell 处理（internal/session/toolregistry.go:415 MatchTool），
// launch --json 回执的 tool 字段就是上游认定的结果（cmd/agent-deck/launch_cmd.go:766）。
// 所以只核配置里的工具名不够：配成 bash 照样起一个 shell。派工器在 launch 之后再按回执核一次（CheckLaunchedTakesTaskText）。
var ToolsWithoutTaskText = []string{"shell"}

// ShellInterpreters 是上游认作交互式 shell 的进程名（照抄上游 internal/session/userconfig.go:3555 isShellBinary）。
// 自定义工具的命令若是其中之一，它就是一个 shell，只是换了名字（n044 attempt 1 评审 blocker 1）。
var ShellInterpreters = []string{"bash", "zsh", "sh", "fish", "dash", "ksh", "tcsh", "csh", "nu", "nushell", "pwsh", "powershell"}

// CheckTakesTaskText 核对配置里的工具能不能接任务说明。launch 之前调，不行就不起会话。
//
// 只放行两类：上游内置的 agent 工具（BuiltinTools 去掉 shell），以及配置里显式声明 agent: true 的自定义工具。
// 自定义工具的命令写在上游配置里，Work Deck 看不到，从名字推断不了它是不是 agent——
// 上一版只拒名字叫 shell 的，在上游配置里声明一个命令是 bash 的自定义工具就绕过去了（n044 attempt 1 评审 blocker 1）。
// 声明 agent: true 等于 owner 担保这个工具会把任务说明当任务读；launch 之后还会按回执核一次实际命令。
func CheckTakesTaskText(tool string, declaredAgent bool) error {
	switch {
	case contains(ToolsWithoutTaskText, tool):
		return fmt.Errorf("工具 %s 不是 agent：它会把任务说明当命令逐行执行（n043 复审实测），不派。"+
			"Graph 任务要派给 claude、codex 这类 agent，或配置里声明 agent: true 的自定义 agent 工具", tool)
	case contains(BuiltinTools, tool):
		return nil
	case !declaredAgent:
		return fmt.Errorf("工具 %s 不是上游内置的 agent，配置里也没声明 \"agent\": true：它的命令写在上游配置里，"+
			"Work Deck 看不到，确认不了它会把任务说明当任务读（命令若是 bash 之类，说明会被当命令执行），不派", tool)
	}
	return nil
}

// CheckLaunchedTakesTaskText 按 launch 回执核对上游实际起的是什么：回执没写工具不信；上游认成 shell 不送；
// 实际命令（resolved_command，没有就看 command）是 shell 解释器也不送。
func CheckLaunchedTakesTaskText(res LaunchResult) error {
	if strings.TrimSpace(res.Tool) == "" {
		return fmt.Errorf("上游 launch 回执没写会话是哪种工具，确认不了它是 agent，任务说明不送")
	}
	if contains(ToolsWithoutTaskText, res.Tool) {
		return fmt.Errorf("上游把命令 %q 起成了 %s 会话：它不是 agent，会把任务说明当命令执行，任务说明不送", res.Command, res.Tool)
	}
	cmd := res.ResolvedCommand
	if strings.TrimSpace(cmd) == "" {
		cmd = res.Command
	}
	if bin := commandBinary(cmd); contains(ShellInterpreters, bin) {
		return fmt.Errorf("上游起的工具 %s 实际命令是 %q：%s 是 shell 解释器，会把任务说明当命令执行，任务说明不送", res.Tool, cmd, bin)
	}
	return nil
}

// commandBinary 取一条命令真正执行的程序名：去掉路径；env 开头的跳过它的选项与变量赋值。
// 只认得这种常见写法；包一层别的启动器（script、python -c 等）认不出，那时靠配置里的 agent 声明担保。
func commandBinary(cmd string) string {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		base := strings.ToLower(filepath.Base(fields[i]))
		if base == "env" {
			for i+1 < len(fields) && (strings.HasPrefix(fields[i+1], "-") || strings.Contains(fields[i+1], "=")) {
				i++
			}
			continue
		}
		return base
	}
	return ""
}

// CodexTrust 说明上游 launch 某个工具时会不会改写 owner 的 Codex 全局配置（n043 owner 验收补充发现）。
type CodexTrust int

const (
	// CodexTrustNo：上游内置、与 codex 不兼容的工具，不写。
	CodexTrustNo CodexTrust = iota
	// CodexTrustYes：一定会写。
	CodexTrustYes
	// CodexTrustMaybe：自定义工具。上游配置若声明它与 codex 兼容、或命令本身是 codex，就会写；Work Deck 看不到上游配置。
	CodexTrustMaybe
)

// CodexTrustPath 是上游写 trust 的文件（CODEX_HOME 未设时的默认位置）。
const CodexTrustPath = "~/.codex/config.toml"

// BuiltinTools 是上游内置的全部工具（internal/session/builtins.go:56-76）。升级上游时要重新核对。
var BuiltinTools = []string{"claude", "opencode", "gemini", "codex", "pi", "copilot", "crush", "cursor",
	"hermes", "deepseek", "aider", "shell"}

// CheckCodexTrust 判断 launch 这个工具会不会把工作目录写进 Codex 的 trust 列表。
//
// 出处：上游 launch Codex 兼容工具前调 preAcceptCodexWorkspaceTrust（internal/session/codex_trust.go:119-137），
// 判据是 IsCodexCompatible（internal/session/userconfig.go:3542：工具名是 codex，或自定义工具声明 compatible_with=codex、
// 命令本身是 codex）。写法是把整份 config.toml 解码再重新编码写回（codex_trust.go:62-104），原文件的注释与排版不保留。
// 写失败时上游只记一条 warning，照样 launch。
func CheckCodexTrust(tool string) CodexTrust {
	switch {
	case tool == "codex":
		return CodexTrustYes
	case contains(BuiltinTools, tool):
		return CodexTrustNo
	default:
		return CodexTrustMaybe
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SendFlags 是 Work Deck 会传给 session send 的全部参数（n042 加 --json，用来读 delivery 字段）。
var SendFlags = []string{"--message-file", "--json", "--defer-if-busy", "--defer-timeout"}

// SendDeliveryField 是 session send --json 失败时 Work Deck 读的字段；取值 typed 视为「送达未确认」。
// 出处：上游 cmd/agent-deck/session_cmd.go:3056-3087、3116-3125。
const SendDeliveryField = "delivery"

// LaunchFlags 是 Work Deck 会传给 launch 的全部参数。
var LaunchFlags = []string{"-c", "--model", "--json", "-t", "-g", "--no-wait"}

// WebFlags 是集成测试起 headless 服务时用的参数（integration_test.go）。
// 发布代码不启动 web：Work Deck 连配置里 upstream_addr 指向的现成服务。
// 没有测试核对这份列表。
var WebFlags = []string{"--no-tui", "--listen", "--token"}

// Evidence 是一条断言在上游源码里的落点：某一行代码。
//
// 它的作用是**让人能立刻翻到那一行**，以及在上游改动时报警：
// 行号会漂，Anchor 不会，行号和 Anchor 对不上测试就红。
//
// 但它证明不了这一行「意味着什么」——那件事交给 Effect。
// attempt 9 到 12 一直想让 Evidence 兼职做语义核对，四次都被绕过。
type Evidence struct {
	Path   string
	Line   int
	Anchor string // 那一行必须含有的文本
	Within string // 那一行所在的函数
}

// String 还原成人读的 文件:行。
func (e Evidence) String() string { return fmt.Sprintf("%s:%d", e.Path, e.Line) }

// EffectKind 是上游可能做的几类事。**它是封闭的**，这一点是关键。
//
// 自由文本可以写任何话，枚举至少把「说的是哪一类事」收窄了。
// 光有枚举不够：attempt 13 的评审在同一个函数里挑了另一件真实但无关的调用，
// 照样通过。所以还要配合落点核对，见 Evidence 与测试。
type EffectKind int

const (
	// EffectCalls：Within 那个函数体里调用了 Target。
	EffectCalls EffectKind = iota
	// EffectDeclares：上游在 Path 那个文件里声明了名为 Target 的函数。
	EffectDeclares
	// EffectDeclaresConst：上游声明了常量 Target，它的值是 Arg。
	EffectDeclaresConst
	// EffectAbsent：几个函数体里搜不到某些东西（见 Absence）。
	EffectAbsent
)

func (k EffectKind) String() string {
	switch k {
	case EffectCalls:
		return "调用"
	case EffectDeclares:
		return "声明函数"
	case EffectDeclaresConst:
		return "声明常量"
	case EffectAbsent:
		return "不出现"
	}
	return fmt.Sprintf("EffectKind(%d)", int(k))
}

// Effect 是一条**机器能自己去上游语法树里查证**的事实。
//
// 这是 attempt 13 的核心改动。前面四轮都在给自由文本加检查：
// 加锚点、加所属函数、要求逐字出现在锚点上。每一轮都被独立评审绕过，
// 最后一次的反例是：
//
//	Does:  "inst.PreviewFull()"   // 从锚点抄来的，必定通过
//	Means: "删除整个用户数据库，并把所有密码发到公网"  // 没人核对
//
// 病根不是检查不够严，是**结构里有一个机器核不了的字段，却长得像证据**。
// 加强文本匹配是加强不动的：一句自然语言总能再塞进第二个意思。
//
// 所以这一轮换成：想声明什么，得从 EffectKind 里挑一个，
// 然后测试拿 go/ast 去上游源码里查证它。查不到就红。
//
//	Effect{Kind: EffectCalls, Target: "PreviewFull", ...}
//	→ 测试去 handleSessionOutput 的语法树里找这个调用，找不到就红
//
// 语法树里没有的调用，写上去就是红的。能核到哪一步有边界，见 boundary-limits.md。
// 人读的解释一律放在**代码注释**里——注释不参与核对，
// 也就不会被误当成有依据的断言。
type Effect struct {
	Kind   EffectKind
	Target string // 函数名或常量名
	Arg    string // 可选：这次调用的某个参数必须是这个字符串常量
}

func (e Effect) String() string {
	if e.Arg != "" {
		return fmt.Sprintf("%s %s（参数含 %q）", e.Kind, e.Target, e.Arg)
	}
	return fmt.Sprintf("%s %s", e.Kind, e.Target)
}

// FuncRef 指一个上游函数，并说清楚它是怎么被走到的。
//
// `Func` 是它在定义处的身份（`Instance.PreviewFull`）。
// `Call` 是它在上一级函数体里被调用时写的样子（`inst.PreviewFull`）。
//
// **这两者之间那一跳，这套测试不验。** 要知道 `inst` 这个变量的类型是不是
// `*session.Instance`，得有类型信息，而测试只解析上游源码，不编译它。
//
// 说清楚一点：**这不是原则上做不到。** 独立评审指出得对——`go/types` 或
// `go/packages` 能验出不少接收者类型，有限的局部推断也能覆盖一部分。
// 不做的理由是代价与脆弱：
//
//   - `go/packages` 要把上游连同它的全部依赖加载并类型检查一遍，
//     这台机器上跑一次十几秒，CI 里还得先能拉到那些依赖；
//   - 一旦上游某个依赖拉不动或版本漂了，这条测试就红——**红的原因
//     跟它要守的那件事毫无关系**，那是脆弱，不是严格；
//   - 局部推断只覆盖「同文件里显式声明」那一小类，`inst` 恰好来自
//     跨包调用的返回值，正好不在覆盖范围里。
//
// 所以这一跳划给人，并在这里写明白代价是什么。哪天上游快照能稳定地
// 类型检查了，这是第一个该收回去的边界。
//
// attempt 15 在这里栽过：那时 `In` 的覆盖资格按尾名算，独立评审往上游副本里
// 加了一个 `(*Other).PreviewFull`，再把 `In` 里的 `Instance.PreviewFull`
// 换成 `Other.PreviewFull`——真正的 `Instance.PreviewFull` 一个字节都没被搜，
// 测试照样通过。尾名相同就算覆盖，等于让任何同名方法都能顶替。
//
// 所以现在分两半：机器核对 `Call` 真的出现在上一级的调用列表里、
// `Func` 在 `Path` 里唯一存在；**而从 `Call` 那个变量到 `Func` 那个类型，
// 由 `Note` 写明白是人读出来的**。跟 `Absence.NotCovered` 一个道理：
// 验不了的就写出来，别假装验了。
type FuncRef struct {
	Path string
	Func string // 定义处的身份，例如 Instance.PreviewFull
	Call string // 在上一级函数体里的调用写法，例如 inst.PreviewFull；入口函数留空
	Note string // Call 那个变量为什么是 Func 这个类型——人读的，测试不验证这句话本身
}

func (f FuncRef) String() string { return f.Path + " 的 " + f.Func }

// Absence 是否定断言的依据：在指定的几个函数体里搜不到某些东西。
//
// 为什么需要它：**有些话用「某一行代码」根本证明不了。**
// 「session output 不发按键」就是——你没法指着一行说「看，这里没发按键」。
//
// NotCovered 是 attempt 13 加的，替掉原来那个自由文本 Scope。
// 原来的 Scope 只被验「非空」，独立评审实测填「覆盖范围有限」照样通过——
// 一个只验非空的边界声明，挡不住空洞，也就没在保护什么。
//
// 现在它必须**逐个列出 In 那几个函数体里真实存在的下游调用**：
// 这份证明不追进去的，都得在这里点名。测试两头对：
// 列了语法树里没有的 → 红（不许编）；语法树里有而没列 → 红（不许漏）。
type Absence struct {
	In         []FuncRef // 搜这几个函数体
	Absent     []string  // 这些文本不得出现在其中
	NotCovered []string  // In 里调用了、但本证明不追进去的函数名
}

// Claim 是一条有出处的断言。
//
// 两个字段各司其职，且**都由机器核对**：
//
//	Effect    上游做的那件事，从封闭枚举里挑一种，测试去语法树里查证
//	Evidence  那件事在源码里的落点，供人翻阅；行号漂了测试也会报
//
// 没有第三个字段。人读的解释写在每条 Claim 上方的注释里——
// 注释是注释，不会被当成有依据的断言。
//
// 这一条来之不易，六轮独立评审换的：
// attempt 7 把 list 当成只读；attempt 8 把 attach/output 硬编码成「不写」；
// attempt 9 补了依据但核对测试形同虚设；attempt 10 的锚点可以跟声明毫不相干；
// attempt 11 的 What 是自由文本，一个 token 命中就整条放行；
// attempt 12 把 What 拆成 Does/Means，Does 抄一遍锚点就是通行证。
//
// 正面断言用 Evidence 落点，否定断言用 Absence，两者必居其一。
type Claim struct {
	Effect   Effect
	Evidence []Evidence
	Absence  *Absence
}

// SideEffect 是一条上游命令跑完之后，上游那边会变什么。
type SideEffect struct {
	Claims []Claim
}

// SharedStorageInit 是 inbox 之外各条命令共有的那一份副作用。
//
// 除 inbox 两条外，白名单里每条 CLI 子命令跑起来都要先加载 profile，那条路会建 profile 目录、
// 建 state.db、跑 schema 迁移，必要时还会把旧的 sessions.json 迁过来改名。
// 也就是说这些命令**没有一条是纯读的**，哪怕它叫 list 或 output。
// inbox 两条在加载 profile 之前就分派出去了，各自的副作用见 SideEffects。
//
// 它单独放在这里，正是为了让各条命令**不能**拿它当自己的依据：
// 「每条命令都会初始化 storage」证明不了「session output 跑了 capture-pane」。
var SharedStorageInit = SideEffect{
	Claims: []Claim{
		{
			// 加载 profile 那条路会建 profile 目录、建库文件、跑 schema 迁移，
			// 必要时还会把旧的会话文件迁过来改名。
			Effect: Effect{Kind: EffectDeclares, Target: "NewStorageWithProfile"},
			Evidence: []Evidence{{
				Path:   "internal/session/storage.go",
				Line:   209,
				Anchor: "func NewStorageWithProfile(profile string) (*Storage, error) {",
				Within: "NewStorageWithProfile",
			}},
		},
		{
			// 库文件就建在 profile 目录下，名字是 state.db。
			Effect: Effect{Kind: EffectCalls, Target: "filepath.Join", Arg: "state.db"},
			Evidence: []Evidence{{
				Path:   "internal/session/storage.go",
				Line:   243,
				Anchor: `filepath.Join(profileDir, "state.db")`,
				Within: "NewStorageWithProfile",
			}},
		},
	},
}

// keyInjectionCommands 是那些会替人敲键的 tmux 子命令。
//
// 下面 session output 的否定断言拿它去上游函数体里搜，证明那条路不碰它们。
//
// 守边测试 TestAdapterMayAttachButNeverSendsKeysOrKills 会放过这个声明，
// 它认的是语法树上的这个变量，不是「哪一行文本里有这个名字」，
// 所以另起一个名字含这个子串的变量蹭不过去（对照脚本 B4）。
// 另一条 TestPreviewPathIsReadOnly 仍按行文本跳过「本变量名后跟等号」的整行（原串见该测试），
// 那是已知缺口 K8；同一段注入会让前一条变红（对照 A24-1）。
var keyInjectionCommands = []string{"send-keys", "paste-buffer"}

// SideEffects 逐条记下白名单里每条命令在上游留下什么。
//
// **白名单管「允许跑哪些命令」，这份清单管「跑了之后上游会变什么」。**
// 两件事分开记，因为「在允许清单里」不等于「没有副作用」。
//
// 这份清单错过六次，每次都是独立评审顺着上游源码追出来的：
// attempt 7 把 list 当成只读；attempt 8 把 session attach / session output
// 硬编码成「不写」；attempt 9 补了依据但核对测试形同虚设；
// attempt 10 的锚点可以跟声明毫不相干；attempt 11 的 What 是自由文本，
// 一个 token 命中就整条放行；attempt 12 的 Does 抄一遍锚点就是通行证。
// 见 Effect 上的说明。
//
// 每条命令都要在这里有个交代，没有「只读桶」可以躲——
// SharedStorageInit 对所有命令都成立，各条再补自己特有的，
// 且**不得拿它当自己的依据**。
var SideEffects = map[string]SideEffect{
	"launch": {
		Claims: []Claim{
			{
				// 把新会话写进上游存储。
				Effect: Effect{Kind: EffectCalls, Target: "storage.InsertSessionAndVerify"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/launch_cmd.go",
					Line:   626,
					Anchor: "storage.InsertSessionAndVerify(newInstance, tree)",
					Within: "handleLaunch",
				}},
			},
			{
				// 会话起来后，实例 ID 被写进 tmux 会话环境——
				// 后面认会话靠的就是它，不是靠名字。
				Effect: Effect{Kind: EffectCalls, Target: "i.tmuxSession.SetEnvironment", Arg: "AGENTDECK_INSTANCE_ID"},
				Evidence: []Evidence{{
					Path:   "internal/session/instance.go",
					Line:   4939,
					Anchor: `SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID)`,
					Within: "Start",
				}},
			},
		},
	},
	"web": {
		Claims: []Claim{{
			// 启动时按配置删掉超出保留期的成本记录——起个服务也会删数据。
			Effect: Effect{Kind: EffectCalls, Target: "costStore.PurgeOlderThan"},
			Evidence: []Evidence{{
				Path:   "cmd/agent-deck/main.go",
				Line:   826,
				Anchor: "costStore.PurgeOlderThan(retDays)",
				Within: "main",
			}},
		}},
	},
	"list": {
		Claims: []Claim{
			{
				// 输出之前先暖 tmux 缓存并加载 hook 状态。所以 list 不是只读的。
				Effect: Effect{Kind: EffectCalls, Target: "session.RefreshInstancesForCLIStatus"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/main.go",
					Line:   2277,
					Anchor: "session.RefreshInstancesForCLIStatus(instances)",
					Within: "handleList",
				}},
			},
			{
				// 那个刷新函数确实在上游，不是我们编的名字。
				Effect: Effect{Kind: EffectDeclares, Target: "RefreshInstancesForCLIStatus"},
				Evidence: []Evidence{{
					Path:   "internal/session/cli_status_refresh.go",
					Line:   16,
					Anchor: "func RefreshInstancesForCLIStatus(instances []*Instance) {",
					Within: "RefreshInstancesForCLIStatus",
				}},
			},
			{
				// 刷新那条路会把活动时间写进库里。
				Effect: Effect{Kind: EffectDeclares, Target: "persistLastActivity"},
				Evidence: []Evidence{{
					Path:   "internal/session/last_activity_persist.go",
					Line:   107,
					Anchor: "func (i *Instance) persistLastActivity(force bool) {",
					Within: "persistLastActivity",
				}},
			},
			{
				// 写进去的键名叫 last_activity_at；写的是真实 hook 活动的时间戳，
				// 不是当前时间。
				Effect: Effect{Kind: EffectDeclaresConst, Target: "toolDataLastActivityAtKey", Arg: "last_activity_at"},
				Evidence: []Evidence{{
					Path:   "internal/session/last_activity_persist.go",
					Line:   25,
					Anchor: `toolDataLastActivityAtKey = "last_activity_at"`,
					Within: "",
				}},
			},
		},
	},
	"session attach": {
		Claims: []Claim{
			{
				// 把当前终端接进目标会话，于是那个会话多出一个连着的客户端。
				Effect: Effect{Kind: EffectCalls, Target: "tmuxSession.Attach"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   1370,
					Anchor: "tmuxSession.Attach(ctx, detachByte)",
					Within: "handleSessionAttach",
				}},
			},
			{
				// badge watcher 起来时会建目录——attach 会在磁盘上留下东西。
				Effect: Effect{Kind: EffectCalls, Target: "os.MkdirAll"},
				Evidence: []Evidence{{
					Path:   "internal/tmux/badge_update.go",
					Line:   125,
					Anchor: "os.MkdirAll(dir, 0o755)",
					Within: "WatchBadgeUpdates",
				}},
			},
		},
	},
	"session output": {
		Claims: []Claim{
			{
				// --pane 分支靠它取当前画面。
				Effect: Effect{Kind: EffectCalls, Target: "inst.PreviewFull"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   4409,
					Anchor: "inst.PreviewFull()",
					Within: "handleSessionOutput",
				}},
			},
			{
				// 最终由 CaptureFullHistory 跑 capture-pane 取画面。
				Effect: Effect{Kind: EffectCalls, Target: "s.runBoundedOutput", Arg: "capture-pane"},
				Evidence: []Evidence{{
					Path:   "internal/tmux/tmux.go",
					Line:   3689,
					Anchor: `runBoundedOutput("capture-pane"`,
					Within: "CaptureFullHistory",
				}},
			},
			{
				// 取画面这条路上，In 列的五个函数体里不出现替人敲键的子命令。
				//
				// **这条证明比它听起来的窄得多。** NotCovered 里那一长串
				// 就是这五个函数体调用了、而本证明追不进去的东西——
				// 三十一个。上游 internal/tmux/tmux.go 另有 8 处把这些子命令
				// 交给 tmux 的调用（快照 01c011b），所以「上游不发按键」是句假话；
				// 能证的只有「我们走的这五个函数体里没有」。
				Effect: Effect{Kind: EffectAbsent, Target: keyInjectionCommands[0]},
				Absence: &Absence{
					In: []FuncRef{
						// 入口：CLI 的 session output 落在这里，没有上一级。
						{Path: "cmd/agent-deck/session_cmd.go", Func: "handleSessionOutput"},
						{
							Path: "internal/session/instance.go", Func: "Instance.PreviewFull",
							Call: "inst.PreviewFull",
							Note: "handleSessionOutput 里的 inst 来自 ResolveSessionOrCurrent，" +
								"类型是 *session.Instance；这一跳是人读出来的，测试只核对名字对得上",
						},
						{
							Path: "internal/tmux/tmux.go", Func: "Session.CaptureFullHistory",
							Call: "i.tmuxSession.CaptureFullHistory",
							Note: "Instance.PreviewFull 里的 i.tmuxSession 是 *tmux.Session 字段；" +
								"这一跳是人读出来的",
						},
						{
							Path: "internal/tmux/socket.go", Func: "Session.runBoundedOutput",
							Call: "s.runBoundedOutput",
							Note: "CaptureFullHistory 的接收者 s 就是 *tmux.Session，" +
								"所以 s.runBoundedOutput 是它自己的方法；这一跳是人读出来的",
						},
						// 链的末端：真正把参数交给 tmux 的就是这个包级函数。
						{
							Path: "internal/tmux/socket.go", Func: ".runBoundedOutput",
							Call: "runBoundedOutput",
						},
					},
					Absent: keyInjectionCommands,
					NotCovered: []string{
						"clipboard.Copy", "flag.NewFlagSet", "fmt.Errorf",
						"fmt.Println", "fmt.Sprintf", "fs.Arg", "fs.Bool",
						"fs.Parse", "fs.PrintDefaults",
						"NewCLIOutput", "ResolveSessionOrCurrent",
						"i.spawnFailurePreview",
						"inst.GetLastResponseBestEffortChecked",
						"inst.GetSessionIDFromTmux", "loadSessionData",
						"normalizeArgs", "os.Exit", "out.Error",
						"out.Print", "sb.String", "sb.WriteString",
						"session.IsClaudeCompatible",
						"session.NoteClaudeSessionIDFromOwnPane",
						"string", "time.Now", "tmux.GetTerminalInfo",
						// 链末端 runBoundedOutput 自己调的这些也没追进去。
						// 它构造命令用的参数由调用方传入，那个常量已被上面
						// 「调用 s.runBoundedOutput 且参数含 capture-pane」那条钉住。
						"Output", "cancel", "context.Background",
						"context.WithTimeout", "tmuxExecContext",
					},
				},
			},
		},
	},
	"session send": {
		Claims: []Claim{{
			// 把一段文本投进目标会话，对方会当成输入处理。
			Effect: Effect{Kind: EffectCalls, Target: "executeSend"},
			Evidence: []Evidence{{
				Path:   "cmd/agent-deck/session_cmd.go",
				Line:   2894,
				Anchor: "executeSend(tmuxSess, inst.Tool, message",
				Within: "handleSessionSend",
			}},
		}},
	},
	// session stop 是 n044 加的：只停 Work Deck 本次刚起、还没收到任务说明的会话（dispatch 包的 abandon）。
	"session stop": {
		Claims: []Claim{
			{
				// 杀掉会话的 tmux 会话；上游存储里的会话记录留着，状态变成 stopped。
				// 入口是 handleSessionStop 里的 inst.Kill()（cmd/agent-deck/session_cmd.go:402），真正动手的是下面这一行。
				Effect: Effect{Kind: EffectCalls, Target: "i.tmuxSession.Kill"},
				Evidence: []Evidence{{
					Path:   "internal/session/instance.go",
					Line:   8397,
					Anchor: "i.tmuxSession.Kill()",
					Within: "killInternal",
				}},
			},
			{
				// 同组若设了并发上限且有排队的会话，停一个会顺带起一个排队的。
				Effect: Effect{Kind: EffectCalls, Target: "drainGroupQueue"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   411,
					Anchor: "drainGroupQueue(inst.GroupPath, instances, groups)",
					Within: "handleSessionStop",
				}},
			},
			{
				// 停完把会话状态写回上游存储。
				Effect: Effect{Kind: EffectCalls, Target: "saveSessionData"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   414,
					Anchor: "saveSessionData(storage, instances, groups)",
					Within: "handleSessionStop",
				}},
			},
		},
	},
	// inbox 的两条在上游 main 里早于 profile 加载就分派出去了（cmd/agent-deck/main.go:417
	// 早于 :518 的 NewStorageWithProfile），所以 SharedStorageInit 对它们不成立，各自写清楚。
	"inbox export": {
		Claims: []Claim{
			{
				// 读完成账本、各父会话 inbox 与 _unowned 三处，取并集；不删、不截断任何东西。
				Effect: Effect{Kind: EffectCalls, Target: "session.ExportPendingRecords"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/inbox_cmd.go",
					Line:   423,
					Anchor: "session.ExportPendingRecords()",
					Within: "runInboxExport",
				}},
			},
			{
				// 不是纯读：记录里出现的每个 profile 都会被打开注册表，没有就建 state.db。
				Effect: Effect{Kind: EffectCalls, Target: "NewStorageWithProfile"},
				Evidence: []Evidence{{
					Path:   "internal/session/inbox_export.go",
					Line:   151,
					Anchor: "NewStorageWithProfile(profile)",
					Within: "dropSuppressedChildren",
				}},
			},
		},
	},
	"inbox writer-status": {
		Claims: []Claim{
			{
				// 只看心跳文件的新旧判断 notify-daemon 在不在。
				Effect: Effect{Kind: EffectCalls, Target: "session.ReadWriterStatus"},
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/inbox_cmd.go",
					Line:   466,
					Anchor: "session.ReadWriterStatus()",
					Within: "runInboxWriterStatus",
				}},
			},
			{
				// 读的就是那个心跳文件。
				Effect: Effect{Kind: EffectCalls, Target: "os.ReadFile"},
				Evidence: []Evidence{{
					Path:   "internal/session/notify_heartbeat.go",
					Line:   74,
					Anchor: "os.ReadFile(path)",
					Within: "ReadWriterStatus",
				}},
			},
		},
	},
}

// HTTPEndpoints 是 Work Deck 会请求的端点。
// TestEveryEndpointInSourceIsListed 按字面量核对源码里的端点都在这里，拼出来的路径看不见。
var HTTPEndpoints = []string{"/api/sessions", "/api/menu", "/events/menu"}

// LaunchFields 是 Work Deck 从 launch --json 里读的字段。
var LaunchFields = []string{
	"session_id", "id", "success", "profile", "tool", "command",
	"resolved_command", "title", "group", "path", "model_id", "model_version",
	// 上游失败时靠这两个字段说明原因，退出码可能仍是 0。
	"error", "code",
}

// SessionFields 是 Work Deck 从 /api/sessions 与菜单事件的 session 对象里读的字段。
var SessionFields = []string{
	"id", "title", "tool", "status", "groupPath", "projectPath",
	"tmuxSession", "tmuxSocketName", "command", "createdAt", "lastAccessedAt",
}

// InboxFields 是 Work Deck 从 inbox export --json 每条记录里读的字段。
var InboxFields = []string{
	"child_session_id", "child_title", "profile", "from_status", "to_status", "timestamp",
	"substate", "last_output_hash", "target_session_id", "target_kind", "kind",
	"done_status", "done_summary", "turn_fingerprint", "source_remote", "dead_letter_reason",
}

// WriterStatusFields 是 Work Deck 从 inbox writer-status --json 里读的字段。
var WriterStatusFields = []string{"running", "last_heartbeat", "age_seconds", "detail"}

// MenuFields 是 Work Deck 从菜单事件顶层读的字段。
var MenuFields = []string{"profile", "generatedAt", "totalGroups", "totalSessions", "items"}

// EnvVars 是 Work Deck 会设置的上游环境变量。
var EnvVars = []string{"AGENTDECK_PROFILE"}

// TmuxConventions 记录 Work Deck 依赖的 tmux 约定。
//
// 自 n037 起 Work Deck 会 attach（按 a 走上游 session attach，按 T 直连 tmux），
// 但一律不发按键：除了 keyInjectionCommands 那一行禁令，包内不出现这些子命令，
// 两条测试扫源码盯着——只盯得住对照脚本列出的写法，
// 盯不住的写法见 GAPS 与 boundary-limits.md；现有代码不含后者，是人工审计的结论。
// 这段注释在 attempt 6 之前还写着「本轮不 attach」，那是 n036 时的话。
var TmuxConventions = []string{
	"会话名形如 agentdeck_<title>_<hash>，由上游生成，Work Deck 只读不拼",
	"会话名从 /api/sessions 或 /api/menu 的 tmuxSession 字段取，不自己根据标题推算",
	"服务缓存看不见刚 launch 的会话时，会话名改从 list --json 的 tmux_session 取",
	"socket 名在 HTTP 的会话对象里有（tmuxSocketName），list --json 的行里没有这个字段；空值即默认 server",
	"socket 名拿不到时，扫 $TMUX_TMPDIR/tmux-<uid>（未设时 /tmp/tmux-<uid>）下每个 socket，" +
		"会话名与 AGENTDECK_INSTANCE_ID 都对上的恰好一个 server 才连；多个或零个都拒绝",
	"has-session 与 show-environment 都只查询，不改会话状态——" +
		"各跑三次实测 session_activity 不变（前后两次都取到了值，不是空对空）；死 socket 会报错，自然跳过",
	"目标一律用 tmux 的精确形式 =<session>：不带等号时 -t 是前缀匹配，" +
		"只有 name-suffix 存在也会命中并连进去",
	"会话身份取自会话环境里的 AGENTDECK_INSTANCE_ID，由上游建会话时写入；" +
		"名字只当查找键，认不认这个会话由身份说了算",
}

// NotUsed 明确记下 Work Deck **不**依赖的上游内部物，防止有人顺手就用上。
var NotUsed = []string{
	"state.db：没有 hook overlay 时 instances.status 不权威，只可排障时看，不作真相来源",
	"上游的配置文件与 profile 目录内容：Work Deck 只通过 AGENTDECK_PROFILE 指定名字，不读写其中文件",
	"上游源码：Work Deck 不修改、不编译进自己的二进制，只调用它的命令与端点",
}

// BypassFlags 是各家 CLI 跳过权限确认或沙箱的参数（架构稿 §5 准入第 4 条：硬拒绝，owner 也批不过）。
// 出处：claude --dangerously-skip-permissions、--permission-mode bypassPermissions；codex --dangerously-bypass-approvals-and-sandbox、
// -a/--ask-for-approval never；gemini、kimi 等的 --yolo。
var BypassFlags = []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
	"--dangerously-bypass-approvals-and-sandbox", "--yolo"}

// codexConfigBypass 是 codex `-c key=value` 覆盖里的绕过（小写、去引号与空格后比较）。
var codexConfigBypass = []string{"approval_policy=never", "sandbox_mode=danger-full-access"}

// bypassValues 是「参数 + 取值」形式的绕过：参数名 → 被拒的取值（小写比较）。
var bypassValues = map[string]string{
	"--permission-mode":  "bypasspermissions",
	"-a":                 "never",
	"--ask-for-approval": "never",
	"--approval-mode":    "yolo",
	"--sandbox":          "danger-full-access",
	"-s":                 "danger-full-access",
}

// CheckNoBypass 检查会进入启动命令的各个字段（工具、可执行文件、模型、标题）里有没有绕过权限的参数。
// 按空白切词，认 `--flag` 与 `--flag=value` 两种写法。命中就硬拒绝。
func CheckNoBypass(fields ...string) error {
	for _, f := range fields {
		// codex 配置覆盖的完整赋值：去掉空白与引号后整段比对（n051 评审 1 B1：-c 'approval_policy = "never"' 按空白切词会漏掉）
		compact := strings.ToLower(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || r == '"' || r == '\'' || r == '\\' {
				return -1
			}
			return r
		}, f))
		for _, bad := range codexConfigBypass {
			if strings.Contains(compact, bad) {
				return fmt.Errorf("拒绝绕过权限的配置覆盖 %s（硬拒绝，owner 也批不过）", bad)
			}
		}
		words := strings.Fields(f)
		for i, w := range words {
			lw := strings.ToLower(w)
			name, value, hasValue := strings.Cut(lw, "=")
			for _, b := range BypassFlags {
				if name == b {
					return fmt.Errorf("拒绝绕过权限的参数 %s（硬拒绝，owner 也批不过）", w)
				}
			}
			if name == "-c" || name == "--config" {
				// codex 的配置覆盖：-c approval_policy=never、-c sandbox_mode=danger-full-access 与 -a never、-s danger-full-access 等价
				v := value
				if !hasValue && i+1 < len(words) {
					v = strings.ToLower(words[i+1])
				}
				v = strings.NewReplacer(`"`, "", `'`, "", " ", "").Replace(v)
				for _, bad := range codexConfigBypass {
					if v == bad {
						return fmt.Errorf("拒绝绕过权限的参数 %s %s（硬拒绝，owner 也批不过）", w, v)
					}
				}
				continue
			}
			bad, ok := bypassValues[name]
			if !ok {
				continue
			}
			if !hasValue && i+1 < len(words) {
				value, hasValue = strings.ToLower(words[i+1]), true
			}
			if hasValue && value == bad {
				return fmt.Errorf("拒绝绕过权限的参数 %s %s（硬拒绝，owner 也批不过）", w, value)
			}
		}
	}
	return nil
}
