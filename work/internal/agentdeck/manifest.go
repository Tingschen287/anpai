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

// Evidence 是一条副作用声明在上游源码里的依据。
//
// 三个字段各有分工，少一个这条依据就证明不了什么：
//
//	Path + Line  指到具体一行
//	Anchor       那一行必须含有的文本——行号会漂，这段文本不会
//	Within       那一行所在的函数——同一段调用在一个文件里常常出现好几次，
//	             光有 Anchor 分不清哪一次
//
// 这个形状是被两轮独立评审逼出来的：
//
//	attempt 9  只写 文件:行，测试只验「行号没超出文件总行数」，
//	           把行号改成同文件任意一行也照样通过。
//	attempt 10 补了 Anchor，但锚点可以跟声明毫不相干——
//	           session output 声称「跑 capture-pane 不发按键」，
//	           依据却指着 NewStorageWithProfile 这个构造函数，测试照样绿。
//
// 所以现在还有两条硬规矩，由 manifest_test 强制：
//
//	一、**依据要指向这条命令自己的副作用点**，不许拿所有命令共有的
//	    storage 初始化（SharedStorageInit）充数。
//	二、**What 里要写出被依据锚住的那个标识符**，测试拿它去锚点里对。
//	    换句话说，描述和依据必须讲同一件事，而不是各说各话。
type Evidence struct {
	Path   string // 上游源码树里的相对路径
	Line   int    // 行号，会漂
	Anchor string // 那一行必须含有的文本，不会随行号漂
	Within string // 那一行所在的函数名，用来分辨同一段调用的不同出现
}

// String 还原成人读的 文件:行。
func (e Evidence) String() string { return fmt.Sprintf("%s:%d", e.Path, e.Line) }

// SideEffect 是一条上游命令跑完之后，上游那边会变什么。
//
// What 不是随便写的说明文字：它必须点名依据锚住的那个标识符，
// 否则 manifest_test 不认——描述与依据讲同一件事，是这份清单唯一的价值。
type SideEffect struct {
	What  string
	Where []Evidence
}

// SharedStorageInit 是所有命令共有的那一份副作用。
//
// 每条 CLI 子命令跑起来都要先加载 profile，那条路会建 profile 目录、
// 建 state.db、跑 schema 迁移，必要时还会把旧的 sessions.json 迁过来改名。
// 也就是说**没有一条命令是纯读的**，哪怕它叫 list 或 output。
//
// 它单独放在这里，正是为了让各条命令**不能**拿它当自己的依据：
// 「每条命令都会初始化 storage」这件事，证明不了「session output 跑了
// capture-pane」。
var SharedStorageInit = SideEffect{
	What: "加载 profile 时 NewStorageWithProfile 建 profile 目录与 state.db、" +
		"跑 schema 迁移、必要时迁移并重命名旧的 sessions.json",
	Where: []Evidence{{
		Path:   "internal/session/storage.go",
		Line:   209,
		Anchor: "func NewStorageWithProfile(profile string) (*Storage, error) {",
		Within: "NewStorageWithProfile",
	}},
}

// SideEffects 逐条记下白名单里每条命令在上游留下什么。
//
// **白名单管「允许跑哪些命令」，这份清单管「跑了之后上游会变什么」。**
// 两件事分开记，因为「在允许清单里」不等于「没有副作用」。
//
// 这份清单错过四次，每次都是独立评审顺着上游源码追出来的：
// attempt 7 把 list 当成只读；attempt 8 把 session attach / session output
// 硬编码成「不写」；attempt 9 补了依据但核对测试形同虚设；
// attempt 10 的锚点可以跟声明毫不相干。见 Evidence 上的说明。
//
// 每条命令都要在这里有个交代，没有「只读桶」可以躲——
// SharedStorageInit 对所有命令都成立，各条再补自己特有的，
// 且**不得拿它当自己的依据**。
var SideEffects = map[string]SideEffect{
	"launch": {
		What: "新建并启动一个会话：storage.InsertSessionAndVerify 把它写进上游存储；" +
			"会话起来后 SetEnvironment 把 AGENTDECK_INSTANCE_ID 写进 tmux 会话环境",
		Where: []Evidence{
			{
				Path:   "cmd/agent-deck/launch_cmd.go",
				Line:   626,
				Anchor: "storage.InsertSessionAndVerify(newInstance, tree)",
				Within: "handleLaunch",
			},
			{
				Path:   "internal/session/instance.go",
				Line:   4939,
				Anchor: `SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID)`,
				Within: "Start",
			},
		},
	},
	"web": {
		What: "起 headless 服务进程；启动时按配置跑 costStore.PurgeOlderThan，" +
			"删掉超出保留期的成本记录",
		Where: []Evidence{{
			Path:   "cmd/agent-deck/main.go",
			Line:   826,
			Anchor: "costStore.PurgeOlderThan(retDays)",
			Within: "main",
		}},
	},
	"list": {
		What: "输出之前先跑 RefreshInstancesForCLIStatus 暖 tmux 缓存并加载 hook 状态，" +
			"经 UpdateHookStatus → persistLastActivity 把 last_activity_at 写进 state.db；" +
			"写的是真实 hook 活动的时间戳，不是当前时间，也不改会话状态",
		Where: []Evidence{
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
			{
				Path:   "internal/session/last_activity_persist.go",
				Line:   107,
				Anchor: "func (i *Instance) persistLastActivity(force bool) {",
				Within: "persistLastActivity",
			},
		},
	},
	"session attach": {
		What: "启动 badge watcher，其中 os.MkdirAll 建 badge 更新目录；" +
			"并给目标 tmux 会话增加一个 attached client",
		Where: []Evidence{{
			Path:   "internal/tmux/badge_update.go",
			Line:   125,
			Anchor: "os.MkdirAll(dir, 0o755)",
			Within: "WatchBadgeUpdates",
		}},
	},
	"session output": {
		What: "走 -pane 分支调 PreviewFull，最终由 CaptureFullHistory 跑 " +
			"tmux capture-pane 取画面；只读画面，不发按键、不改会话状态",
		Where: []Evidence{
			{
				Path:   "cmd/agent-deck/session_cmd.go",
				Line:   4409,
				Anchor: "inst.PreviewFull()",
				Within: "handleSessionOutput",
			},
			{
				Path:   "internal/tmux/tmux.go",
				Line:   3689,
				Anchor: `runBoundedOutput("capture-pane"`,
				Within: "CaptureFullHistory",
			},
		},
	},
	"session send": {
		What: "executeSend 把一段文本投进目标会话，对方会当成输入处理",
		Where: []Evidence{{
			Path:   "cmd/agent-deck/session_cmd.go",
			Line:   2894,
			Anchor: "executeSend(tmuxSess, inst.Tool, message",
			Within: "handleSessionSend",
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
// 但一律不发按键：包内不出现任何注入按键的 tmux 子命令，两条测试扫源码盯着。
// （这里不写出那个子命令的名字——守边测试是按文本扫的，写出来它自己就会红。）
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
