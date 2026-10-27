# 安排 Anpai（Go 部分）

「安排 Anpai」（命令 `deck`；后台服务 `deckd` 是它的 `deck daemon` 子命令，不是单独的可执行文件）的源码在本目录，Go 写的，有自己的 `go.mod`
（模块路径 `github.com/Tingschen287/terminal-deck/work`，仓库改名后没有改）。
仓库根的 [`README.md`](../README.md) 是面向使用者的入口：是什么、平台与依赖、安装、启动与常用按键、配置路径。
本文是它的细节补充：各部分怎么工作、有哪些约束，以及更早的 Work Deck 界面（`work`）还留着什么。

仓库里另有两个 Python 写的早先启动器 `ai` 与 `host`，与本目录互不影响，见根 README 最后一节。

支持 macOS 和 Linux；本机前提是装好 tmux（要带 `--enable-sixel` 编译，侧栏的图标才显示）和 upstream agent-deck；用 Graph 项目时 projectctl 要 Python 3.11 及以上，环境变量 `DECK_PROJECTCTL_PYTHON` 可指定解释器。

## 名字

本产品公开名是「安排 Anpai」（窄处只写「Anpai」；字标 Anpai 用 Geist SemiBold，「安排」用思源黑体，logo 仍是 A4）。
原名 Agent Deck 与 upstream agent-deck（`github.com/asheshgoplani/agent-deck`）撞名，2026-10-15 owner 选定新名（adr_032）。
只改了界面上看得到的名字：命令 `deck`（含 `deck daemon`）、服务名 `deckd`、配置与状态路径（`$XDG_CONFIG_HOME/deck`、`$XDG_STATE_HOME/deck`）、环境变量、键位、
tmux socket 与会话名、Go 包名 `internal/agentdeck`（它指 upstream agent-deck 的适配层）都没变。

- 本目录文档里写 **upstream agent-deck** 或 agent-deck 的，指上游那个程序。
- `internal/agentdeck` 里和本文说的「本仓自己那个同名的 Agent Deck」，指旧的 Python 启动器 `ai`，它不在这次改名范围内。

## 构建

要 Go 1.27.1 或更新（`go.mod`）。

```bash
go build -o deck ./cmd/deck    # 安排 Anpai：只有这一个可执行文件，deckd 就是 `deck daemon`
go build -o work .             # 旧界面 Work Deck，见本文最后一节
```

## 功能概览

按验收的节点（n049～n069）说，每一项的细节在后面各节。

| 节点 | 内容 |
|---|---|
| n049 | 四区布局与按键：布局用 deck 自己的 tmux server；左栏会话侧栏、标签条、中间嵌套 client、右栏与下栏面板；显隐与接管 |
| n050 | 后台服务 `deckd`：唯一写入者，按调用者认身份分权限；面板：需要你决策、额度、进度、通知、agent 在干嘛 |
| n051 | 主 agent 与派工：`deck agent …`、`deck project add`；预留名额、预算包络与 cost 决定；子 agent 汇报转告主 agent |
| n055 | 入口：一条命令 `deck up`；侧栏按 `m` 起主 agent；设置里按 `n` 选目录新建项目；CLI 停在启动确认上时让给 owner；`agent-deck list --json` 读会话状态 |
| n056 | 品牌显示：Sixel logo、字母徽章、额度面板各家 logo 与重置时间；按终端能力自动选画法 |
| n057 | agent 设置：全局可用的 agent 与模型档位、每个项目每档有序多选；加入与移出 agent；Codex 授权记忆可撤销 |
| n058 | 改名「安排 Anpai」（adr_032） |
| n059 | F9 设置界面重做：三页（项目、全局 agent、授权）、光标行整行高亮、80×24 不截断、改动自动保存 |
| n069 | iTerm2 上的 logo（合成背景，不再是实心方块）、空闲页用 32×16 色块、终端标签标题是品牌名；F9「面板」页挂上、拔掉面板，立即作用于正在运行的布局；进度面板按原型分段并跟着正在看的会话走 |

## 布局与侧栏（n049、n055）

deck 自己的 tmux server（socket 在 `$TMUX_TMPDIR/tmux-<UID>/deck`，没设 `TMUX_TMPDIR` 时是 `/tmp`；无前缀键、无状态栏、开鼠标）。
默认尺寸：左栏 32 列，右栏 40 列，下栏 6 行，标签条 1 行，其余给中间（`layout.go`）。

- 左栏 `deck sidebar`：按项目列会话（★ 主 agent，圆点：绿在跑、黄等你输入、灰空闲、红出错、空心为未知）。
- 中间上方一行 `deck tabs`：打开的会话，点标签换焦点，点 ✕ 放回后台。
- 中间 `deck center`：连到 agent-deck 那台 tmux server 的嵌套 client，每次都先以只读连上。
- 右栏与下栏是面板，默认右栏挂「需要你决策」与「额度」，下栏挂通知；「进度」与「agent 在干嘛」默认不挂。

点左栏的会话把它换进中间。子 agent 默认只读，主 agent 的第一条 brief 确认送达后可写。
CLI 启动时停在确认（目录信任等）上也对 owner 放开，由 owner 在中间自己选（n055）。

布局上的按键都在布局 server 上截获，不进中间的 CLI：

| 键 | 作用 |
|---|---|
| F2 | 接管中间正在显示的子 agent（切走、隐藏或 Ctrl-Q 即自动交还；交还没完成时再按 F2 重新接管） |
| Ctrl-Q | 把中间正在看的会话放回后台（会话照常运行；任何 CLI 都收不到 Ctrl-Q） |
| F5 / F6 / F7 | 显示或隐藏左栏 / 右栏 / 下栏（程序不退出，尺寸复原） |
| F8 | 把排队等着转告主 agent 的汇报立刻送出（`deck reports deliver-now`） |
| F9 | agent 设置 |
| F10 | 键盘焦点回到中间窗格 |

左栏：`↑↓`（或 `k` `j`）选，`Enter`（或 `o`）打开，`h` 放回后台，`m` 为选中的项目起主 agent（项目标题行也能选；
与 `deck agent start-main` 同一套准入），`Esc` 或 F10 回到中间；鼠标可点会话、点行尾的眼睛、滚轮选择。

- 中间 client 断开（例如显示的会话被关掉）会以只读重连，并把断开时显示的会话放回后台；连续 3 次在 3 秒内断开就停下等你按回车。
  每次重连通知 deckd 的结果记在 `$XDG_STATE_HOME/deck/center.log`。deckd 的每个视图操作都绑定到开始时的那个 client，
  `deck center` 每次起 client 之前先等 deckd 确认没有进行中的视图操作；deckd 在而没确认时中间显示原因、不起新 client。
- deckd 跑在布局的后台窗口里：关掉布局 server（例如 `tmux -S … kill-server`）deckd 随之停止，agent 会话照常在 agent-deck server 上跑，
  这期间决定、通知、派送都不可用。再 `deck up` 会把 deckd 起来；接管中的会话按重启恢复处理（保持接管、自动派送暂停，等你交还）。

## 入口 `deck up`（n055）

```bash
go build -o deck ./cmd/deck
./deck up                      # 一条命令打开四区布局（已有布局就直接接上；每次都查 deckd，没在跑就在布局的后台窗口里起它）
./deck settings -check         # 只读自查设置文件（升级前先跑；不需要 deckd，不写任何东西）
./deck daemon                  # 只起后台服务 deckd：$XDG_STATE_HOME/deck/deckd.sock（0600）
./deck whoami                  # deckd 把当前进程认成谁
```

`deck up` 的参数：`-graphics`（logo 画法）、`-upstream`（改连现成的 agent-deck headless 服务）、`-layout-socket`、`-agent-socket`。
`deck daemon` 的参数：`-profile`（agent-deck profile，默认 `deck`）、`-layout-socket`、`-agent-socket`、`-dashboard`（Token 看板地址，
默认 `http://127.0.0.1:8899`）、`-projectctl`（Graph 控制器脚本）、`-upstream`。

会话运行状态：deckd 默认调 `agent-deck list --json` 读（不写会话状态；含已归档会话），不需要 agent-deck 的 headless 服务，
没有 token、没有端口（owner 在设计复审选这个方案：经 TCP 端口发 token 无法保证对面是自己的服务，WSL2 mirrored 网络下尤甚）。
间隔最少 3 秒、按上次耗时的 10 倍放大：每个会话（含已停的）约 6–7 ms，会话积累多了状态刷新会变慢，
可用 `agent-deck -p deck remove <id>` 清理不要的旧会话。读不到时圆点显示「未知」，原因在 `deck` 健康检查的 `activity_error` 里。
仍可用 `deck up -upstream 127.0.0.1:8420`（或 `DECK_AGENTDECK_ADDR`）改连现成的 headless 服务，token 取 `WORKDECK_AGENTDECK_TOKEN`。

## 面板的挂上与拔掉（n069）

F9 设置的「面板」页（数字键 `5`）列出面板注册表 `panels.yaml` 里的每一块和它在哪一栏（右栏或下栏），空格或 `⏎` 挂上或拔掉光标所在的一块。

- **立刻生效，不用重建布局**：deckd 收到改动先存进 `panels.yaml`（deckd 是它的唯一写入者），再把正在运行的布局调成一致——
  挂上就在那一栏新开一个窗格（排在这一栏最下面或最右面），拔掉就拆掉窗格、面板程序随之结束。其余窗格的程序一个都不重启。
  这一栏被 F6 / F7 整栏隐藏着时，新挂上的面板同样停放着，再按 F6 / F7 一起回来。
- **每块改一次存一次**：不走设置页的整份自动保存。别处（另一个窗口、手改 `panels.yaml`）改过，进「面板」页或按 `R` 会重新读。
  两个窗口同时挂拔：deckd 串行处理，最后都按存储里的最新状态收敛。
- **布局没跟上**（例如布局被关了、tmux 出错）：改动仍然存下，界面写明原因；下次 `deck up` 建布局就按 `panels.yaml` 来。
- 拔掉额度面板只是不显示，额度门（派工前的额度检查）照旧工作。
- 不做面板上用鼠标点的「×」：键盘在「面板」页操作。
- 自己加面板：在 `panels.yaml` 里照着已有的写一项（`name`、`command`、`region: right|bottom`、`on`），重启 deckd（关掉布局再 `deck up`）后它出现在「面板」页，显示它的 `name`。

### 进度面板

按原型：一条分段进度条（已验收绿、阻塞琥珀、失败红、其余暗灰）加数字格（已验收、阻塞、失败、其余），下面列「其余」里各状态的明细、预算与取数问题。

- **项目跟着正在看的会话走**：`deck panel progress` 不带 `-project` 时，每 2 秒向 deckd 要一次，deckd 按中间窗格此刻显示的会话找它所属的项目。
  在左栏点另一个项目的会话，面板几秒内换成那个项目。没有在看的会话（中间是空闲页），或会话不属于设置里的项目，面板写一句说明，不是报错。
- **`-project <id>` 仍可用，并且优先**：写了它就固定看那个项目，不跟随（`panels.yaml` 里写 `deck panel progress -project <id>` 的老写法不受影响）。
- 项目没有配 `graph_root` 时写「这个项目没有接 Graph」；进度数据读 Graph 控制器的只读快照，不会改任何东西。
- 默认不挂：在「面板」页挂上「项目进度」即可。

## 品牌显示（n056、n058、n069）

- logo：`DECK_GRAPHICS=sixel|blocks|none`（或 `deck up -graphics`）；不设时 `deck up` 问终端（CSI 16 t 与 DA1）：声明支持 Sixel 且知道一格多少像素就用 Sixel，
  否则用品牌色字母徽章（不自动用半格色块：4×2 格只有 4×4 点，认不出）。
- WSL 下 Windows Terminal 报给 Linux 的像素尺寸永远是 0，tmux 3.4 只认这个、为 0 就把 Sixel 画成「SIXEL IMAGE」占位。
  `deck up` 接上布局后把问到的格子像素补进自己的终端，窗口每次改大小（WSL 会清零）后再补，并请布局整屏重画。只对经 `deck up` 接上的客户端有效：直接 `tmux attach` 的不补。
- 复用一个用别的画法起的布局时，`deck up` 会给布局 server 补上 Sixel 特性，并把本次画法写进状态目录的 `graphics`；已在运行的侧栏与额度面板每 1–2 秒读它切换。
  同时接着几个客户端时，最后运行 `deck up` 的说了算（tmux 里一个窗格只有一种画法）；要换回来，关掉那个窗口重开，或在支持 Sixel 的终端另开一个标签页运行 `deck up`。
- deck 自己的 logo（暂用，adr_031）与品牌色青柠 `#b4f546`：侧栏标题前（Sixel 时）、中间空闲页（嵌套 tmux 里画不了 Sixel，用半格色块：放得下用 32×16 格，放不下用 16×8 格，再放不下不画；真机上 16×8 格认不出，32×16 清楚）。
- **iTerm2（macOS）**（n069）：iTerm2 把 Sixel 里「没画」的像素涂成图里最后定义的那种颜色，只有一种颜色的 A4 logo 就成了实心方块。所以认出是 iTerm2（环境变量 `TERM_PROGRAM=iTerm.app` 或 `LC_TERMINAL=iTerm2`）时，
  `deck up` 多问一次终端的背景色（OSC 11），把 logo 合成到这个背景上、每个像素都画出来（背景用单独的、不量化的颜色寄存器）；选中的会话行合成到行底色上。背景色写进状态目录的 `graphics-bg`，侧栏与额度面板每 1–2 秒读它。
  问不到背景色时自动退回字母徽章（实心方块比没有图更糟）；`deck up -graphics sixel` 强制时尊重你的选择。别的终端（Windows Terminal 等）不多问任何东西，编码一个字节都没变。
- **终端标签标题**（n069）：布局 server 开了 tmux 的 `set-titles`，标签标题是品牌名（中文界面「安排 Anpai」，英文界面「Anpai」）；`deck up` 每次都对已经在运行的布局补设，所以复用旧布局也生效，不用改 iTerm2 的配置。
  标题文字按 `agents.yaml` 里的界面语言取，在 F9 里换语言后侧栏几秒内跟着改。`deck up` 接上布局前把终端当前标题压栈（`CSI 22;0 t`），退出时弹栈（`CSI 23;0 t`）：终端认这个栈时标题恢复原样；不认的终端会留着品牌名，关掉标签页即可。
  iTerm2 的标签文字后面括号里的进程名（例如 `(tmux)`）是 iTerm2 自己的配置项，不归 deck 管。
- 产品名取舍只有一条（`deckui.BrandFor`）：可用显示列数 ≥ 10 写「安排 Anpai」，否则写「Anpai」；侧栏标题与空闲页共用。
- 空闲页跑在 agent-deck 那台 tmux 上，升级二进制后它每 3 秒检查启动时的路径是否换成了另一个可执行文件，是就自己 `exec` 成新的。

## deckd（n050）

- **唯一写入者**：deck 的状态（注册表、决策、通知、Codex 授权记忆、汇报队列、lease）只由 deckd 写，放在 `$XDG_STATE_HOME/deck/`，与旧 `work` 的目录分开。
- **同一个 agent-deck profile 只许一个写入者**：deckd 与旧 `work` 都先取 `$XDG_STATE_HOME/agent-deck-writers/<profile>.lock`，取不到的一方只读。deck 默认用 profile `deck`。
- **按调用者分权限**：deckd 凭 `SO_PEERCRED` 与进程链认出调用者在哪个 tmux 窗格——布局窗格是 owner，注册表里的会话是主 agent 或子 agent，
  其他是未知（只能读额度、健康状态与 whoami 自身诊断，不能写）。项目与创建者一律由服务端推导。同一系统用户下这防的是模型越权与误操作，不防恶意代码。
- **需要 Linux 6.5 及以上**：连接身份经 `SO_PEERPIDFD` 绑定到对端进程本身，防进程号复用。内核不支持时，一切调用者按未知处理
  （`caller.go` 的 `classify`），而未知调用者只能读健康状态、`whoami` 与额度：侧栏读会话、设置页读设置、决策与通知面板都要 owner 或主 agent 身份，
  所以旧内核上布局基本用不了，不是退化成只读模式。
- **owner 身份只来自布局窗格**：`caller.go` 的 `classifyPane` 只把布局 server 上的窗格认成 owner。布局里没有让人敲命令的 shell 窗格，
  所以 `deck project add`、`deck agent start-main`、`deck reports deliver-now` 这些要 owner 的命令，在普通终端里会被拒（403）；
  人用界面（F9 新建项目、侧栏 `m`、F8），命令行形式给布局内的进程和 agent 会话里的 agent 用。
  各接口要求的角色在 `internal/deckd/server.go`、`agents.go`、`reports.go` 的 `need(…)` 调用里。
- **预留名额**（`deckstore.Store.Reserve`，`deck agent spawn` 用）：
  - 主、子会话都必须带启动请求指纹：`request_id`（字母数字与 `._:-`，最长 64）、`dir`（规整的绝对路径，最长 300 字节）、
    `task_sha256` 与 `args_sha256`（64 位小写十六进制，分别是任务正文与实际启动参数的 sha256）。原样校验，不清洗，不合格就拒绝。
  - `request_id` 在整个注册表里只能用一次：不论记录现在是什么状态都占着它，重复请求返回错误；要重试就用新的请求 id。
    启动失败（`LaunchFailed`，非 uncertain）会释放名额；uncertain 不是终态，继续占名额，之后仍可能被确认为启动成功。
  - 超过预算包络时要先建一个 cost 决定，scope 用 `SpawnScope(请求, 项目设置)`；owner 批准后先 `BeginExecute` 把决定置为执行中（deckd 在 `Reserve` 前自己调，`agents.go`），再在有效期内以同一请求调 `Reserve(请求, 决定 id)`；没经过 `BeginExecute` 的决定 `Reserve` 会拒绝。
    目录、任务、参数、请求 id、项目目录、包络版本任一变了，例外都不能用；用一次即标为 applied。
- **额度**：deckd 的额度门只读 Token 看板 `127.0.0.1:8899/api/data`；取不到、过期或没有窗口的来源按「未知」处理，不编数字。
  额度面板（`/v1/quota`）在此之外补几个只供显示的来源：Claude 读 Claude Code 状态栏脚本缓存的官方用量（超过一天或已过重置时刻的不用），
  Antigravity 按看板的各额度池各一条；Kimi 暂时隐藏（`dashquota.DisplayTools`）。取不到的来源在面板上不显示。每个来源前有它的 logo，窗口后写重置时间。
  OpenCode 免费模型没有限额接口，按看板的撞墙日均值算当天用量百分比，标「日」，次日 0 点重置。
- **面板**：`deck panel <decisions|quota|progress|notifications|activity> [-project id]`。决策面板里 `↑↓` 选、数字键作答；
  `progress` 必须带 `-project`，默认不挂。面板开关在 `panels.yaml`。

## 主 agent 与派工（n051、n055）

主 agent 是项目里负责派活的会话；子 agent 由主 agent 按档位派出，在侧栏项目下列出。工具都经 deckd，输出 JSON，身份由 deckd 按调用进程判定：

```text
deck agent whoami
deck agent spawn --tier <档位> | --model <模型> --title <标题> --task-file <文件|-> [--dir 目录] [--graph-node 节点]
                 [--exception 决定 --request-id 原请求 id]
deck agent list | output <id> | send <id> --text <内容>
deck agent escalate --kind <cost|config|push|disagree|deviation> --summary <说明> [--options a,b] [--recommend x] [--scope k=v,…]
deck agent decisions
deck agent wait [--timeout 2m]          主 agent 自己等子 agent 的汇报（上限 10 分钟）
deck agent report [--done] --text <内容>   子 agent 汇报
deck agent start-main --project <项目>   owner 起主 agent
deck project add --id <id> --name <名字> --path <规整的绝对路径> [--main <agent:档位>，默认 claude:frontier] [--agents a,b] [--model 档位=模型,…]
deck reports deliver-now [--project <id>]
```

退出码：0 完成；3 等 owner 批准（输出里有决定与下一步）；1 出错。档位是 `economy`、`balanced`、`frontier`。

- 超出项目预算包络的派工要先建 cost 决定，owner 在右栏批准后用 `--exception <决定> --request-id <原请求 id>` 重试同一条命令。
- **第一条消息前先看画面**（n055）：CLI 停在启动确认上就不送，记为等 owner 处理（侧栏与通知说明）；这期间不给它发话、不往主 agent 推送汇报；
  确认消失、画面稳定后 deck 自动送一次。deckd 重启后，主 agent 接着等；子 agent 的任务正文没保存，提示 owner 重派。
- **额外参数**（`agents.yaml` 的 `extra_args`）：只能声明白名单里的参数（`agentdeck.declaredAllow`），随启动真正传给 CLI
  （claude 经上游 `launch --extra-arg`，codex 经 `-c "codex 参数…"`），启动后照旧按白名单核对实际命令行。
- **不替 owner 改上游配置**：起 Claude 前核对 agent-deck 的 `config.toml`，`[claude]` 下必须显式写 `dangerous_mode = false`，
  `allow_dangerous_mode`、`auto_mode` 不能为 true；codex、gemini 的 `yolo_mode` 不能为 true。不满足时拒绝启动并说明怎么改。

## F9 设置（n057、n059）

布局里按 F9 在布局 server 的新窗口里打开设置（`deck settings`），五页，`1`～`5` 或 `Tab` 换页，`q` 或 `Esc` 关闭：

- **项目**：每个项目勾选用哪些 agent（空格）、主 agent（空格轮换）、三档各一个有顺序的模型清单（`⏎` 打开多选，`[` `]` 调顺序）；`←→` 换项目；`n` 选目录新建项目（从 `deck up` 所在的目录开始）。
- **全局 agent**：全局可用的 agent（`a` 从本机装了的 CLI 里加入，`⏎` 打开改模型与档位、移出）。
- **授权**：撤销记住的 Codex 授权（`d`，再按一次确认）。
- **通用**：界面语言（中文 / English，空格或 `⏎` 切换，存上就立刻换，不用重启）。
- **面板**（n069）：右栏与下栏的面板——需要你决策、项目进度、额度、agent 在干嘛、通知，以及你在 `panels.yaml` 里自己加的。空格或 `⏎` 挂上、拔掉，见下一节。

`R` 重新读取。改动自动保存，对之后起的会话生效，已在跑的会话不变；存不上时 `s` 重试。档位在界面里叫最强、日常、便宜，
文件、命令行 `--tier` 与 deckd 接口仍用 `frontier`、`balanced`、`economy`。
界面语言在「通用」页切换（中文 / English）；全新安装第一次运行跟随系统语言（`LC_ALL`、`LC_MESSAGES`、`LANG` 里第一个非空值以 `zh` 开头用中文，`C`、`POSIX`、`C.UTF-8` 用英文），并把它写进 `agents.yaml` 的 `ui_lang`；升级的老用户（`agents.yaml` 已存在、没有 `ui_lang`）保持中文，文件不改。升级二进制后要先关掉布局再重新 `deck up`：旧的 deckd 不认 `ui_lang`，语言切换要等新 deckd 起来才生效。

### 配置文件

`$XDG_CONFIG_HOME/deck/agents.yaml`（全局可用的 agent 与模型档位，首次运行生成默认值）、`projects.yaml`（每个项目选用哪些 agent、三档各用哪个模型、预算包络）、`panels.yaml`（面板开关）。

- 两份设置文件每次保存写同一个 `generation`（比上一代大，按时间走，是十几位的数），并把上一代留成 `.prev`。保存在两次写之间失败会留下一新一旧：
  读取时代数不一致就拒绝（deckd 不启动），按提示把较新的那份换回 `.prev`（没有 `.prev` 说明它是第一次写，删掉即回到默认），或核对后把两份的 generation 改成一样。
  手改时保持两份的 generation 一样即可；旧文件两份都没有这个字段，算同一代。generation 手改成负数或超过 2^53，deck 不启动并提示改成 0。
- **保存**：整份保存要带上读到的 `generation`，别处（另一个设置窗口、`deck project add`、手改）在这期间存过就拒绝（409），按 `R` 重新读取。
  deck（deckd）每次启动后，启动前打开的设置窗口第一次保存都会被拒，按 `R` 重新读取再存。系统时钟往回拨、或文件里的 generation 比现在的时间还大时，这条保证不成立。
- **每档有序多选**（owner：「排第一的优先，额度用尽就换下一个」）：`projects.yaml` 的 `models` 每档是一个列表（旧的一档一个字符串照读，保存时写成列表）。
  只说档位派活时按顺序取第一个额度没显示用尽的（看板确知用尽才跳过，未知照常用）；frontier 全都用尽时照用第一个、交给额度门上报 owner，其他档位说明原因、不派。
  点名某个模型时不看清单：勾选的 agent 的模型都能点名用。主 agent 用它那一档清单里它自己的第一个模型。`deck agent` 起子 agent 前问 deckd（`GET /v1/resolve`）这一档现在会派哪个。
- 模型 id 只能是字母数字和 `. _ / : @ + -`（它会进 CLI 的命令行），保存时与派工时都查。
- 默认 `agents.yaml`（只有这个文件不存在时才生成，已有的设置文件一个字节都不改）含 claude、codex、grok、antigravity（`agy`）、opencode、kimi 六项；
  codex 的默认三档是 `gpt-6-astra`（最强）、`gpt-6.1-sol`（日常）、`gpt-6-luna`（便宜），kimi 是 `kimi-code/k3`（日常）与 `kimi-for-coding`（便宜）。
  设置里「加入」认识的 CLI 是 claude、codex、opencode、grok、`agy`、kimi、gemini。加模型时 codex 会跑一次 `codex debug models` 列出候选（只列 `visibility` 为 `list` 的），
  grok、agy、opencode 会跑一次它们自己的 `models`（都是 20 秒为限；列不出来就退回手填并说明原因），claude、kimi、gemini 没有列模型的命令，手填。
  命令（binary）、agent 标记、启动额外参数不在界面里改，只显示；要改手改 `agents.yaml`。
- **找 CLI 用的 PATH（会话 PATH）**：agent-deck 起的会话用的是启动它的进程（deckd）的 PATH，不读 rc 文件、也不读 tmux server 的环境。
  `deck daemon` 因此在**启动时后台读一次**登录交互 shell 的 PATH（`$SHELL -l -i -c`，5 秒超时，结束时清理探针原进程组里的后台进程，rc 里的杂音不会被当成结果），
  与自己进程的 PATH 合并（登录 shell 里 deckd 没有的目录加在最前面，deckd 自己的目录保持原来的顺序跟在后面），得到一份快照。
  设置里显示的「已装」「路径」「版本」、Codex trust 门查的可执行文件与版本、列模型都按这份快照查，起会话时也把这份 PATH 交给 `agent-deck launch`。
  「能不能执行」按当前进程的有效身份判断（与 Go 的 `exec.LookPath`、shell 的查找一致，unix 上走 `faccessat(X_OK)`），不是「任意一类用户有执行位」：
  文件被改成 `0645`（属主不能执行、其他人能）时它不算找到，设置里的「已装」、trust 门、启动前的绑定核对都会跳过它，三处用的是同一个判断。
  - **什么时候会运行你的登录启动文件（rc）：只有两个时机。** ① deckd 启动时后台读一次；② 你在 F9 的加入页或 agent 页按 **p**（「刷新会话 PATH」）。
    其余一律不运行：打开设置窗口、进加入页和 agent 页、派工、列模型，用的都是当前快照；快照不会过期、不会自己重读（以前的「成功 5 分钟、失败 30 秒、设置里 10 秒」过期重读已去掉）。
    装了新 CLI 或改了 PATH 之后，在这两页按一次 p 即可；页面上有一行说明这个键会运行登录启动文件。
  - **按 p 之后：** 在后台重读一次（界面照常可用，读的时候继续显示旧结果并说「正在刷新」），读完更新已装与版本。读的过程中再按（同一窗口连按、几个窗口同时按）合并为同一次，一次只允许一个读在跑。
    刷新失败时保留上一份好快照，页面显示失败原因；deckd 启动时读失败则退回 deckd 自己的 PATH，两页上橙色一行写明原因，并提示按 p 重试。
  - **会不会因此卡住派工：** 不会。读在 deckd 自己的后台 goroutine 里，派工、列模型、显示只读当前快照，不等正在跑的读；一次派工取一份快照，trust 门与 launch 用同一份。
    **最坏等待**只有 deckd 刚启动、第一次读还没完成时有人用快照：等这一次，上限 5 秒探针超时加 0.5 秒收拾，约 5.5 秒；之后任何时候都不等。
  - **trust 门核准的文件就是启动的文件：** launch 只带工具名，上游按名字在交给它的 PATH 里再找一次，所以「PATH 字符串相同」并不保证找到同一个文件。
    Codex 授权通过后，deckd 把**核准文件所在的目录放到交给上游的 PATH 最前面**（其余目录顺序不变），并在启动前核对：按这份 PATH 找到的必须就是核准的那个文件，不是就拒绝启动。
    这样核准之后、启动之前，更靠前的目录里新装的同名文件顶替不了它。副作用：该目录里的其他命令在这一次启动的会话里也排到前面。
    **只绑定路径**：核准的那个路径上的文件被替换（内容换了、符号链接被改指别处）是既有的信任边界——授权一直以（项目、工具、目录、路径、版本）为界，不核对文件内容，这里不管。
  - **能力边界：** 探针清理的是原进程组；rc 里自己 `setsid`、双 fork 脱离进程组的后代清不掉，不承诺清掉整棵派生树。
  不改你的 shell 配置；rc 里要避开 deck 的解析，可以判断环境变量 `ANPAI_PATH_PROBE`。
- **grok、Antigravity、Kimi Code** 不是 agent-deck 内置工具，要在 agent-deck 的 `config.toml` 里登记 `[tools.grok]`（`command = "grok"`）、`[tools.antigravity]`（`command = "agy"`）、`[tools.kimi]`（`command = "kimi"`），
  只写 `command`（可加 `icon`），不写 `dangerous_mode`、`wrapper`、`compatible_with`。模型经 `-c "<工具> --model <模型>"` 传给 CLI（上游 `launch` 不接受自定义工具的 `--model`）。
- **升级前自查**：项目 `path` 要求规整的绝对路径、不超过 300 字节、不含换行与制表符等控制字符。旧配置不满足时 deckd 启动失败并说明原因；先跑 `deck settings -check`。

## 与 upstream agent-deck 的对接

对接并逐条核对过的是 upstream agent-deck v1.15.0（提交 `01c011b`）。命令行子命令走白名单，白名单精确到第二级，清单之外的子命令 adapter 拒绝执行；
`internal/agentdeck/MANIFEST.md` 有逐条的表，每条带上游源码依据，测试拿依据去上游快照核对。适配层不拼 tmux 的 kill、respawn 与发键命令（`TestAdapterMayAttachButNeverSendsKeysOrKills`）。

## 测试

```bash
go vet ./...
go test -count=1 ./...
```

旧界面的夹具在 `testdata/workdeck-fixture`，是一个七个节点分别停在七种状态的假项目，账本由控制器亲自写出，和真项目同构。要重建它，
脚本需要 Graph 控制器 `projectctl.py`；找不到时（默认按作者的工作区布局找）用环境变量 `PROJECTCTL` 指给它：

```bash
PROJECTCTL=<projectctl.py 的路径> bash testdata/rebuild-fixture.sh
```

## 旧界面 Work Deck（`work`，n035～n047）

`work` 是更早的界面，把一个 Graph Engineering 项目的进度摆在一屏里：每个节点什么状态、正在跑的那个是第几次尝试、跑了多久、三个维度的预算还剩多少、有哪几条在等你拍板。
它与 `deck` 并存，用各自的配置与状态目录（旧界面用 `~/.config/work-deck/config.json` 一类路径）。能力：看一个 Graph 项目的全貌，开工并派工，进入会话，派评审，看评审结论。

```bash
go build -o work .
./work --project <项目根目录>
./work --project ./testdata/workdeck-fixture --once 100          # 不进全屏，只把当前这一屏打到标准输出（宽度 100）
./work --project <项目根目录> --once 100 --fast                   # 只走投影那一段，不逐个问控制器：快，代价是有节点显示「查询中」
```

找不到 Graph 控制器时用环境变量 `WORKDECK_PROJECTCTL` 指路（或 `--projectctl`）。

| 按键 | 做什么 |
|---|---|
| `↑↓` `Enter` `Tab` | 选节点、看详情、展开已验收 |
| `d` | ready / rework 节点：开工并派工；running 且没绑会话：补派；submitted：派评审 |
| `a` | 把终端交给会话，走上游的 `session attach` |
| `T` | 绕开上游，直连会话所在的 tmux |
| `p` | 只读预览会话画面，不接管 |
| `v` | 看评审结论（取自评审报告文件）和一条可复制的 `projectctl review` 命令 |
| `H` | 对选中节点的会话：看介入摘要，按 `y` 确认交还（`internal/ui/lease.go`） |
| `i` | 打开决策队列（配置目录不可用时提示没有接队列） |
| `r` `q` | 刷新、退出。`Esc` 只关预览、详情和弹窗，在主界面上不退出 |

**对 Graph 控制器这一侧只有一个写入口：`projectctl start`**，只在上游会话已经起来、确认是 agent 之后才调；提交、验收与其他状态变更一律不写。
拿不到的数据显示成「未知」，不填一个看起来合理的数字。`internal/projectctl` 的测试逐条盯着这一点。
对 upstream agent-deck 那一侧会写（建会话、投递 brief 本来就是写操作），逐条见 `internal/agentdeck/MANIFEST.md`。

派工的几条规矩（n044、n047）：

- **shell 不接任务。** 任务说明送进 shell 会被当成命令逐行执行；派之前只放行上游内置的 agent 工具，以及配置里写了 `"agent": true` 的自定义工具，launch 之后再按回执核一次。
- **先起会话，再开工，最后送任务。** launch 失败不开工；开工失败或任务送不进去，把刚起的空会话停掉。开工出岔子先对账，对不上就说「结果不明、可能已经开工」。
- **旧绑定不覆盖，写绑定的进程至多一个。**
- **派 codex 之前先问你**：upstream agent-deck 启动 codex 前会把工作目录写进 `~/.codex/config.toml` 的 trust 列表并整份重写那个文件，界面把这件事摆出来，按 `y` 才派。
- **评审结论取自报告文件**，给出 sha256，画面只作提示；界面上的结论不是验收事实，记账前核对报告。
- 执行 brief 写明项目根、`projectctl brief` 与 `projectctl submit` 命令；评审 brief 要求评审者不记账。

数据来源：节点清单、依赖、预算上限取自 `work-graph.yaml`；每个节点此刻的状态现问 `projectctl brief`；attempt、执行者、开工时刻、阻塞原因、预算已用取自控制器生成的 `PROJECT-STATUS.md`；
`projectctl verify-chain` 用来判断那份投影还新不新。投影落后于账本时，来自投影的每一个数字都转成未知。`elapsed` 这一维度的已用量永远显示为未知：控制器不导出它，从事件时间戳自己推是猜，所以不猜。
控制器对项目目录加了锁，并发查询不会更快，所以打开时先用状态投影点亮屏幕，投影覆盖不到的节点先标「查询中」，再在后台逐个补上。

> 能力变了，README 要跟着变：这一节曾经写「只读」，在加了派工、进入会话与开工写入口之后过了很久才被评审指出。
