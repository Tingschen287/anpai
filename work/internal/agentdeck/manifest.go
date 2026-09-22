package agentdeck

import "fmt"

// 本文件是 Work Deck 对 upstream agent-deck 的依赖清单，逐条列出所用的子命令、
// 命令行参数、HTTP 端点、依赖的输出字段与 tmux 约定。
//
// 它存在的理由：上游一改，这里就是核对基准。清单里的每一项都有测试盯着，
// 代码用了清单外的东西、或者清单列了代码根本不用的东西，测试都会失败，
// 所以它不会写完就烂掉。人读版本在同目录的 MANIFEST.md。

// UpstreamVersion 是本清单核对过的上游版本。
const UpstreamVersion = "Agent Deck v1.15.0"

// UpstreamCommit 是上游快照的提交号。
const UpstreamCommit = "01c011b"

// CLIJSONFields 是 Work Deck 从 list --json 的行里读的字段。
//
// 注意它和 SessionFields 不是一套：命名是 snake_case，而且没有 socket 名。
var CLIJSONFields = []string{"id", "title", "tool", "status", "path", "group", "tmux_session", "profile", "archived"}

// LaunchFlags 是 Work Deck 会传给 launch 的全部参数。
var LaunchFlags = []string{"-c", "--model", "--json", "-t", "-g", "-m", "--no-wait"}

// WebFlags 是 Work Deck 启动 headless 服务时用的全部参数。
var WebFlags = []string{"--no-tui", "--listen", "--token"}

// Evidence 是一条断言在上游源码里的依据：某一行代码。
//
// 四个字段各有分工，少一个这条依据就证明不了什么：
//
//	Path + Line  指到具体一行
//	Anchor       那一行必须含有的文本——行号会漂，这段文本不会
//	Within       那一行所在的函数——同一段调用在一个文件里常常出现好几次
type Evidence struct {
	Path   string
	Line   int
	Anchor string
	Within string
}

// String 还原成人读的 文件:行。
func (e Evidence) String() string { return fmt.Sprintf("%s:%d", e.Path, e.Line) }

// FuncRef 指一个上游函数。
type FuncRef struct {
	Path string
	Func string
}

func (f FuncRef) String() string { return f.Path + " 的 " + f.Func }

// Absence 是否定断言的依据：在指定的几个函数体里搜不到某些东西。
//
// 为什么需要它：**有些话用「某一行代码」根本证明不了。**
// 「session output 不发按键」就是——你没法指着一行说「看，这里没发按键」。
//
// 为什么必须写 Scope：函数体级的搜索**不覆盖它调用的其他函数**。
// 上游 internal/tmux/tmux.go 里有 21 处调用 keyInjectionCommands 里的子命令，
// 所以「上游不发按键」是句假话；能证的只有「我们走的这几个函数体里没有」。
// 边界说不清楚的否定断言，等于没有依据——Scope 空着测试就红。
type Absence struct {
	In     []FuncRef // 搜这几个函数体
	Absent []string  // 这些文本不得出现在其中
	Scope  string    // 这条证明覆盖到哪、不覆盖哪
}

// Claim 是一条有出处的断言。
//
// **Does 与 Means 的分工是这套清单的关键，不是多此一举。**
//
//	Does   上游做的那件事，写成它在代码里的名字。测试要求它**逐字**出现在
//	       自己的锚点上——所以 Does 不可能是假话：编不出上游没有的符号。
//	Means  给人看的解释。**它不构成断言，测试不验证它。**
//	       里面提到的技术符号仍会被核对，防止它偷偷变成断言；
//	       但中文那部分的真假，只能靠人读和评审，机器管不了。
//
// 为什么非得这么分：**测试判断不了一句自然语言的真假。**
//
// attempt 11 把副作用写成一段自由文本 What 加一串锚点，测试验的是
// 「What 与所有锚点至少共享一个长标识符」。独立评审举的反例：
//
//	What: "删除整个用户数据库，然后调用 PreviewFull 读取画面"
//
// PreviewFull 命中，前半句一路绿灯。而改成「每条 Claim 一句话」之后
// 这个反例**照样通过**——把两个断言塞进同一句话就行了。
// 顺着这条路继续加强 token 匹配是死路：自然语言总能塞进第二个意思。
//
// 所以换个方向：不再假装机器能核对整句话，而是把机器真能核对的那部分
// （符号）单独拎出来当断言，剩下的明确标成解释。
// **一份说明自己讲清楚哪部分有依据、哪部分没有，比一份假装全都有依据的更可信。**
//
// 正面断言用 Evidence（指着某一行），否定断言用 Absence（在函数体里搜不到），
// 两者必居其一。
type Claim struct {
	Does     string
	Means    string
	Evidence []Evidence
	Absence  *Absence
}

// SideEffect 是一条上游命令跑完之后，上游那边会变什么。
type SideEffect struct {
	Claims []Claim
}

// SharedStorageInit 是所有命令共有的那一份副作用。
//
// 每条 CLI 子命令跑起来都要先加载 profile，那条路会建 profile 目录、
// 建 state.db、跑 schema 迁移，必要时还会把旧的 sessions.json 迁过来改名。
// 也就是说**没有一条命令是纯读的**，哪怕它叫 list 或 output。
//
// 它单独放在这里，正是为了让各条命令**不能**拿它当自己的依据：
// 「每条命令都会初始化 storage」证明不了「session output 跑了 capture-pane」。
var SharedStorageInit = SideEffect{
	Claims: []Claim{{
		Does:  "NewStorageWithProfile",
		Means: "加载 profile 时建 profile 目录与库文件、跑 schema 迁移，必要时迁移并重命名旧的会话文件",
		Evidence: []Evidence{
			{
				Path:   "internal/session/storage.go",
				Line:   209,
				Anchor: "func NewStorageWithProfile(profile string) (*Storage, error) {",
				Within: "NewStorageWithProfile",
			},
			{
				Path:   "internal/session/storage.go",
				Line:   243,
				Anchor: `filepath.Join(profileDir, "state.db")`,
				Within: "NewStorageWithProfile",
			},
		},
	}},
}

// keyInjectionCommands 是那些会替人敲键的 tmux 子命令。
//
// **这行是它们在本包里唯一出现的地方**，而且是作为「被禁止的东西」出现：
// 下面 session output 的否定断言拿它去上游函数体里搜，证明那条路不碰它们。
// 守边测试 TestAdapterMayAttachButNeverSendsKeysOrKills 扫源码时放过这一行，
// 其余任何一处出现都会让它变红。
//
// 为什么要这么绕：证明「不做某件事」，就得先说得出那件事叫什么；
// 而守边测试是按文本扫的，说出来它自己就会红。上一轮为了绕开这点，
// 注释里写的是「这里不写出那个子命令的名字」——于是那条否定断言
// 就没有任何东西核对它。把名字收进一个有名有姓的常量，两边都成立。
var keyInjectionCommands = []string{"send-keys", "paste-buffer"}

// SideEffects 逐条记下白名单里每条命令在上游留下什么。
//
// **白名单管「允许跑哪些命令」，这份清单管「跑了之后上游会变什么」。**
// 两件事分开记，因为「在允许清单里」不等于「没有副作用」。
//
// 这份清单错过五次，每次都是独立评审顺着上游源码追出来的：
// attempt 7 把 list 当成只读；attempt 8 把 session attach / session output
// 硬编码成「不写」；attempt 9 补了依据但核对测试形同虚设；
// attempt 10 的锚点可以跟声明毫不相干；attempt 11 的 What 是自由文本，
// 一个 token 命中就整条放行。见 Claim 上的说明。
//
// 每条命令都要在这里有个交代，没有「只读桶」可以躲——
// SharedStorageInit 对所有命令都成立，各条再补自己特有的，
// 且**不得拿它当自己的依据**。
var SideEffects = map[string]SideEffect{
	"launch": {
		Claims: []Claim{
			{
				Does:  "InsertSessionAndVerify",
				Means: "把新会话写进上游存储",
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/launch_cmd.go",
					Line:   626,
					Anchor: "storage.InsertSessionAndVerify(newInstance, tree)",
					Within: "handleLaunch",
				}},
			},
			{
				Does:  "SetEnvironment",
				Means: "会话起来后把 AGENTDECK_INSTANCE_ID 写进 tmux 会话环境",
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
			Does:  "PurgeOlderThan",
			Means: "启动时按配置删掉超出保留期的成本记录",
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
				Does:  "RefreshInstancesForCLIStatus",
				Means: "输出之前先暖 tmux 缓存并加载 hook 状态",
				Evidence: []Evidence{
					{
						Path:   "cmd/agent-deck/main.go",
						Line:   2277,
						Anchor: "session.RefreshInstancesForCLIStatus(instances)",
						Within: "handleList",
					},
					{
						Path:   "internal/session/cli_status_refresh.go",
						Line:   16,
						Anchor: "func RefreshInstancesForCLIStatus(instances []*Instance) {",
						Within: "RefreshInstancesForCLIStatus",
					},
				},
			},
			{
				Does:  "persistLastActivity",
				Means: "那条路把 last_activity_at 写进库里；写的是真实 hook 活动的时间戳，不是当前时间",
				Evidence: []Evidence{
					{
						Path:   "internal/session/last_activity_persist.go",
						Line:   107,
						Anchor: "func (i *Instance) persistLastActivity(force bool) {",
						Within: "persistLastActivity",
					},
					{
						Path:   "internal/session/last_activity_persist.go",
						Line:   25,
						Anchor: `toolDataLastActivityAtKey = "last_activity_at"`,
						Within: "(文件顶层",
					},
				},
			},
		},
	},
	"session attach": {
		Claims: []Claim{
			{
				Does:  "tmuxSession.Attach",
				Means: "把当前终端接进目标会话，于是那个会话多出一个连着的客户端",
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   1370,
					Anchor: "tmuxSession.Attach(ctx, detachByte)",
					Within: "handleSessionAttach",
				}},
			},
			{
				Does:  "os.MkdirAll",
				Means: "badge watcher 起来时建出 badge 更新目录",
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
				Does:  "PreviewFull",
				Means: "-pane 分支靠它取当前画面",
				Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   4409,
					Anchor: "inst.PreviewFull()",
					Within: "handleSessionOutput",
				}},
			},
			{
				Does:  "capture-pane",
				Means: "最终由 CaptureFullHistory 跑它取画面",
				Evidence: []Evidence{{
					Path:   "internal/tmux/tmux.go",
					Line:   3689,
					Anchor: `runBoundedOutput("capture-pane"`,
					Within: "CaptureFullHistory",
				}},
			},
			{
				Does:  keyInjectionCommands[0],
				Means: "这条路上的两个函数体里不出现它们——也就是取画面不会顺手替人敲键",
				Absence: &Absence{
					In: []FuncRef{
						{Path: "cmd/agent-deck/session_cmd.go", Func: "handleSessionOutput"},
						{Path: "internal/tmux/tmux.go", Func: "CaptureFullHistory"},
					},
					Absent: keyInjectionCommands,
					Scope: "只覆盖这两个函数体本身，不覆盖它们调用的其他函数。" +
						"上游 internal/tmux/tmux.go 里另有 21 处调用这些子命令，" +
						"所以「上游不发按键」是句假话，能证的只有这一段路。",
				},
			},
		},
	},
	"session send": {
		Claims: []Claim{{
			Does:  "executeSend",
			Means: "把一段文本投进目标会话，对方会当成输入处理",
			Evidence: []Evidence{{
				Path:   "cmd/agent-deck/session_cmd.go",
				Line:   2894,
				Anchor: "executeSend(tmuxSess, inst.Tool, message",
				Within: "handleSessionSend",
			}},
		}},
	},
}

// HTTPEndpoints 是 Work Deck 会请求的全部端点。
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

// MenuFields 是 Work Deck 从菜单事件顶层读的字段。
var MenuFields = []string{"profile", "generatedAt", "totalGroups", "totalSessions", "items"}

// EnvVars 是 Work Deck 会设置的上游环境变量。
var EnvVars = []string{"AGENTDECK_PROFILE"}

// TmuxConventions 记录 Work Deck 依赖的 tmux 约定。
//
// 自 n037 起 Work Deck 会 attach（按 a 走上游 session attach，按 T 直连 tmux），
// 但一律不发按键：除了 keyInjectionCommands 那一行禁令，包内不出现这些子命令，
// 两条测试扫源码盯着。
// 这段注释在 attempt 6 之前还写着「本轮不 attach」，那是 n036 时的话。
var TmuxConventions = []string{
	"会话名形如 agentdeck_<title>_<hash>，由上游生成，Work Deck 只读不拼",
	"会话名从 /api/sessions 或 /api/menu 的 tmuxSession 字段取，不自己根据标题推算",
	"服务缓存看不见刚 launch 的会话时，会话名改从 list --json 的 tmux_session 取",
	"socket 名只有 /api/menu 给，list --json 的行里没有这个字段；空值即默认 server",
	"socket 名拿不到时，扫 $TMUX_TMPDIR（或 /tmp/tmux-<uid>）下每个 socket 跑 has-session，" +
		"证明会话名只在一个 server 上才连；多个或零个都拒绝",
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
