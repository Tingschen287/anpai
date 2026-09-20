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

// LaunchFlags 是 Work Deck 会传给 launch 的全部参数。
var LaunchFlags = []string{"-c", "--model", "--json", "-t", "-g", "-m", "--no-wait"}

// WebFlags 是 Work Deck 启动 headless 服务时用的全部参数。
var WebFlags = []string{"--no-tui", "--listen", "--token"}

// HTTPEndpoints 是 Work Deck 会请求的全部端点。
var HTTPEndpoints = []string{"/api/sessions", "/events/menu"}

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
	"tmuxSession", "command", "createdAt", "lastAccessedAt",
}

// MenuFields 是 Work Deck 从菜单事件顶层读的字段。
var MenuFields = []string{"profile", "generatedAt", "totalGroups", "totalSessions", "items"}

// EnvVars 是 Work Deck 会设置的上游环境变量。
var EnvVars = []string{"AGENTDECK_PROFILE"}

// TmuxConventions 记录 Work Deck 依赖的 tmux 约定。
//
// 本轮只读到会话名，不 attach、不发按键。attach 是 n037 的事。
var TmuxConventions = []string{
	"会话名形如 agentdeck_<title>_<hash>，由上游生成，Work Deck 只读不拼",
	"会话名从 /api/sessions 的 tmuxSession 字段取，不自己根据标题推算",
}

// NotUsed 明确记下 Work Deck **不**依赖的上游内部物，防止有人顺手就用上。
var NotUsed = []string{
	"state.db：没有 hook overlay 时 instances.status 不权威，只可排障时看，不作真相来源",
	"上游的配置文件与 profile 目录内容：Work Deck 只通过 AGENTDECK_PROFILE 指定名字，不读写其中文件",
	"上游源码：Work Deck 不修改、不编译进自己的二进制，只调用它的命令与端点",
}
