package agentdeck

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

// SideEffects 逐条记下白名单里哪些命令有写副作用，以及写的是什么。
//
// 白名单管「允许跑哪些命令」，这份清单管「跑了之后上游会变什么」。
// 两件事分开记，因为「在允许清单里」不等于「没有副作用」——
// attempt 7 把 `list` 当成只读加了进去，独立评审顺着上游源码追出它会写 SQLite。
//
// 没有列在这里的白名单命令，意思是它不写上游的持久化状态。
var SideEffects = map[string]string{
	"launch": "新建并启动一个会话，写入上游存储",
	"web":    "起一个 headless 服务进程",
	"list": "读之前会先跑 RefreshInstancesForCLIStatus 暖 tmux 缓存并加载 hook 状态，" +
		"经 UpdateHookStatus → persistLastActivity 把 last_activity_at 写进 state.db；" +
		"只是活动时间戳，不改会话状态、不停会话",
	"session send": "把一段文本投进目标会话，对方会当成输入处理",
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
	"has-session 只查询，不改会话状态——实测跑三次 session_activity 不变；死 socket 会报错，自然跳过",
}

// NotUsed 明确记下 Work Deck **不**依赖的上游内部物，防止有人顺手就用上。
var NotUsed = []string{
	"state.db：没有 hook overlay 时 instances.status 不权威，只可排障时看，不作真相来源",
	"上游的配置文件与 profile 目录内容：Work Deck 只通过 AGENTDECK_PROFILE 指定名字，不读写其中文件",
	"上游源码：Work Deck 不修改、不编译进自己的二进制，只调用它的命令与端点",
}
