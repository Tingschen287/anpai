# Work Deck

把一个 Graph Engineering 项目的进度摆在一屏里：每个节点什么状态、正在跑的那个
是第几次尝试、跑了多久、三个维度的预算还剩多少、有哪几条在等你拍板。

它和 Agent Deck、Host Deck 住在同一个仓，但**不是**同一个程序：那两个是 Python
写的启动器，这个是 Go 写的终端应用，有自己的 `go.mod`，互不影响。用 Go 是因为
它将来要对接的 **upstream agent-deck**（`github.com/asheshgoplani/agent-deck`）也是 Go，
同语言省一层来回。

注意这里说的是上游那个开源项目，和本仓自己的 Agent Deck 重名但不是一个东西，
约定见项目里的 `2.code/link.md`。

## 现在能做什么

看一个 Graph 项目的全貌，开工并派工，进入会话，派评审，看评审结论。

| 按键 | 做什么 |
|---|---|
| `↑↓` `Enter` `Tab` | 选节点、看详情、展开已验收 |
| `d` | ready / rework 节点：开工并派工；running 且没绑会话：补派；submitted：派评审 |
| `a` | 把终端交给会话，走上游的 `session attach` |
| `T` | 绕开上游，直连会话所在的 tmux |
| `p` | 只读预览会话画面，不接管 |
| `v` | 看评审结论（取自评审报告文件）和一条可复制的 `projectctl review` 命令 |
| `r` `q` | 刷新、退出。`Esc` 只关预览、详情和弹窗，在主界面上不退出（n047） |

**对 Graph 控制器这一侧只有一个写入口：`projectctl start`。**

- 派 ready / rework 节点时经它开工，拿账本给的 attempt。它只在上游会话已经起来、确认是 agent 之后才调；
  launch 失败时 Graph 一点不动
- 提交、验收与其他状态变更一律不写——按 `v` 看结论，记账命令由你自己运行
- 拿不到的数据显示成「未知」，不填一个看起来合理的数字

这不是口头约定，`internal/projectctl` 的测试逐条盯着：只读命令白名单只有四条，
唯一的写方法是 `Client.Start`，只跑 `start`；`exec` 只准出现在两个 adapter 包内，
包外任何地方碰账本文件都会让测试失败。

### 派工的几条规矩（n044）

- **shell 不接任务。** 任务说明送进 shell 会被当成命令逐行执行。派之前只放行两类：上游内置的 agent 工具，
  以及配置里写了 `"agent": true` 的自定义工具（它的命令在上游配置里，Work Deck 看不到，要 owner 显式担保）。
  launch 之后再按回执核一次：上游认成 shell 的（例如配成 `bash`）、实际命令是 bash / sh / zsh 这类 shell 解释器的，停掉会话再拒。
  包一层别的启动器（`python -i`、`script` 等）认不出，那时只靠 `agent` 声明担保。
- **先起会话，再开工，最后送任务。** launch 失败不开工；开工失败、或任务送不进去，把刚起的空会话停掉
  （上游 `session stop`，要 Work Deck 的自动许可，有人连着就不停）。送达未确认的照旧落绑定，交给你看。
- **开工出岔子先对账。** 开工前记下账本事件数；回执没到、不可信（例如子进程超时）时，看事件数变没变、状态投影里
  这个节点是不是正由这次的 actor 跑着：确定没开工就说没开工；对账确认开工了就照常派；对不上就说「结果不明、可能已经开工」，
  请你按 `r` 刷新，节点 running 就按 `d` 补派同一个 attempt。开工前核对不了账本就不开工。
- **旧绑定不覆盖。** 账本刚为某个 attempt 开工，绑定存储里却已有一条（夹具重建前留下的），它挪进 `bindings.json` 的
  `superseded` 留档，不删；回执里点名那个旧会话，它可能还在上游跑。写绑定的进程至多一个：同一配置目录的第二个 Work Deck
  打开写入权记账时拿不到文件锁，派不了工。
- **派 codex 之前先问你。** upstream agent-deck 启动 codex 前会把工作目录写进 `~/.codex/config.toml` 的
  trust 列表，并整份重写那个文件。界面把这件事摆出来，按 `y` 才派。认不出的自定义工具说「可能」。
- **评审结论取自报告文件。** 会话画面谁都能往里写字，所以 `v` 读报告、给出 sha256，画面只作提示；
  两边不一致会明说。界面上的结论不是验收事实，记账前核对报告。

### 任务说明写什么（n047）

- **执行者拿到完整命令。** 执行 brief 写明项目根、读合同的 `projectctl brief` 命令和提交用的 `projectctl submit`
  命令，路径带引号可直接粘贴。actor 就是这次开工记进账本的那个（补派时用账本里记着的执行者），照抄就能提交。
  只说「用控制器」时，n045 五轮的执行者每一轮都去上级目录翻找控制器、猜 actor。
- **只在项目根内读。** brief 要求按合同的 read_scope 读，要读项目根以外的东西先停下来问。这是给执行者的规矩，
  Work Deck 不强制；越出工作目录时靠 agent 自己的权限弹窗拦。
- **评审者不记账。** 评审 brief 写明不要运行 `projectctl` 的 review、submit 或任何写账本的命令；owner 按 `v` 看过报告后自己记账。

**对 upstream agent-deck 那一侧会写。** 派工要建会话、派评审要投递 brief，
这些本来就是写操作。哪条命令在上游留下什么，`internal/agentdeck/MANIFEST.md`
有一张逐条的表，每条带上游源码依据，测试拿依据去上游快照核对。

> 这一节在 n035 时写的是「只读。它读，不写」，那时确实如此。
> n036 加了派工、n037 加了进入会话与派评审之后它就不对了，却一直留到
> 第八轮评审才被指出来。n042 加了开工写入口，这一节仍写着「对 Graph 仍然是只读的」，
> 又留到 n043 复审才被指出（n044 改）。**能力变了，README 要跟着变。**

## 跑起来

```bash
go build -o work .

# 看一个项目
./work --project ~/ai/00.projects/infra-visible-agent-orchestration

# 不进全屏，只把当前这一屏打到标准输出（宽度 100）
./work --project ./testdata/workdeck-fixture --once 100

# 同上，但只走投影那一段，不逐个问控制器：快，代价是有节点显示「查询中」
./work --project ~/ai/00.projects/infra-visible-agent-orchestration --once 100 --fast
```

按键：`r` 立刻刷新，`q` 退出。界面每 5 秒自己刷一次。

打开时是两段式的：先用状态投影把屏幕点亮，等 owner 的条目、运行中的 attempt 和
预算立刻就能看；投影覆盖不到的节点先标「查询中」，再在后台逐个补上。

找不到 Graph 控制器时用环境变量指路：

```bash
export WORKDECK_PROJECTCTL=~/ai/01.assets/project-control/projectctl.py
```

## 数据从哪来

| 要什么 | 来源 | 为什么 |
|---|---|---|
| 节点清单、依赖、预算上限 | `work-graph.yaml` | 静态规格，不是账本 |
| 每个节点此刻的状态 | `projectctl brief` | 控制器现算的，最准 |
| attempt、执行者、开工时刻、阻塞原因、预算已用 | `PROJECT-STATUS.md` | 控制器生成的状态投影，只有这里有 |
| 账本是否完好、最新事件时间 | `projectctl verify-chain` | 用来判断上面那份投影还新不新 |

状态投影是控制器写命令的副产物，Work Deck 只在开工时触发一次写命令，平时不会去刷新它，所以它随时可能
落后于账本。每次取数都会拿账本最新事件时间跟投影的生成时间比一比，一旦发现投影
落后，**来自投影的每一个数字都转成未知**，屏幕上直接说明落后到什么程度。节点状态
不受影响，因为那是现问控制器要的。

## 为什么要分两段

控制器对项目目录加了锁，一次 `brief` 在一个四十多节点的真实项目上要两三秒，而且
**并发完全不管用**——实测并发 1、4、8、16 跑同样 12 个节点都是 33 到 35 秒。
所以唯一能省时间的办法是少问几次：

- 投影的三张分类表已经写明了 ready、running、submitted、blocked、failed 这五类
  节点的状态，这部分一次都不问。
- accepted 与 superseded 是终态，不会自己变回去，问过一次就记住。
  failed 不是终态：控制器允许把它 resume 回 ready，所以每次都重新问。
- 剩下的才去问，而且放在屏幕点亮之后。

在 43 个节点的真实项目上，先行那一段 3.3 秒出屏，比原先干等两分钟强得多。

## 已知缺口

`elapsed` 这一维度的**已用量**永远显示为未知。控制器目前不导出它：预算表里只有
worker rounds、frontier calls、tokens 和 cost 四列，没有耗时；命令行也没有任何
只读子命令会把它吐出来。从事件时间戳自己推一个出来是猜，所以这里不猜。上限能读到，
照常显示。

## Agent Deck v1：deck 与 deckd（n050 起，建设中）

`work` 是旧界面，n052 前保留。新的 `deck` 命令（`cmd/deck`）是 v1 的入口，设计见项目 `1.design/2026-10-10-agent-deck-v1-architecture.md`。

```bash
go build -o deck ./cmd/deck
./deck up                      # 一条命令打开四区布局（已有布局就直接接上；每次都查 deckd，没在跑就在布局的后台窗口里起它）
./deck settings -check         # 只读自查设置文件（升级前先跑；不需要 deckd，不写任何东西）
./deck daemon                  # 只起后台服务 deckd：$XDG_STATE_HOME/deck/deckd.sock（0600）
./deck whoami                  # deckd 把当前进程认成谁
```

**布局**（n049）：deck 自己的 tmux server（`$TMUX_TMPDIR/tmux-UID/deck`，无前缀键、无状态栏）。左栏 `deck sidebar` 按项目列会话
（★ 主 agent，圆点：绿在跑、黄等你输入、灰空闲、红出错、空心为未知），中间上方一行 `deck tabs` 是打开的会话，中间是连到
agent-deck tmux server 的嵌套 client（`deck center`），右栏与下栏是面板。点左栏的会话把它换进中间；子 agent 默认只读，
主 agent 的第一条 brief 确认送达后可写；CLI 启动时停在确认（目录信任等）上时也对 owner 放开，由 owner 在中间自己选（n055）。
按键都在布局 server 上截获，不进中间的 CLI：

| 键 | 作用 |
|---|---|
| F2 | 接管中间正在显示的子 agent（切走、隐藏或 Ctrl-Q 即自动交还；交还没完成时再按 F2 重新接管） |
| Ctrl-Q | 把中间正在看的会话放回后台（会话照常运行；任何 CLI 都收不到 Ctrl-Q） |
| F5 / F6 / F7 | 显示或隐藏左栏 / 右栏 / 下栏（程序不退出，尺寸复原） |
| F9 | agent 设置：全局可用清单（没装的标灰）、项目勾选 agent 与三档模型、主 agent、撤销记住的 Codex 授权；按 n 选目录新建项目（从 deck up 所在的目录开始） |
| m（左栏） | 为选中的项目起主 agent（项目标题行也能选；与 `deck agent start-main` 同一套准入） |
| F10 | 键盘焦点回到中间窗格 |

- logo：`DECK_GRAPHICS=sixel|blocks|none`（或 `deck up -graphics`）；不设时 `deck up` 问终端（CSI 16 t 与 DA1，n056）：声明支持 Sixel 且知道一格多少像素就用 Sixel，否则用品牌色字母徽章（不再自动用半格色块：4×2 格只有 4×4 点，认不出）。
  - WSL 下 Windows Terminal 报给 Linux 的像素尺寸永远是 0，tmux 3.4 只认这个、为 0 就把 Sixel 画成「SIXEL IMAGE」占位。`deck up` 接上布局后把问到的格子像素（Windows Terminal 回答 10×20）补进自己的终端，窗口每次改大小（WSL 会清零）后再补，并请布局整屏重画。只对经 `deck up` 接上的客户端有效：直接 `tmux attach` 的不补。
  - 复用一个用别的画法起的布局时，`deck up` 会给布局 server 补上 Sixel 特性；各窗格里程序的画法要关掉布局重开才换。
  - deck 自己的 logo（暂用，adr_031）与品牌色青柠 `#b4f546`：侧栏标题前（Sixel 时）、中间空闲页（嵌套 tmux 里画不了 Sixel，用 16×8 格半格色块）。
- 中间 client 断开（例如显示的会话被关掉）会以只读重连，并把断开时显示的会话放回后台；连续 3 次在 3 秒内断开就停下等你按回车。
  每次重连通知 deckd 的结果记在 `$XDG_STATE_HOME/deck/center.log`。通知带着这个 client 的进程号：client 已退出或已不是中间现在的 client，deckd 拒绝执行（日志里记「已退出」或 409）。重建的 client 与旧的共用中间窗格的终端，所以：deckd 的每个视图操作都绑定到开始时的那个 client，每条改动 client 的 tmux 命令执行后读回，落到了新 client 上就把它还原到起点（deck-idle、只读）；`deck center` 每次起 client 之前先等 deckd 确认没有进行中的视图操作（deckd 没在运行则不用等），deckd 在而没确认时中间显示原因、不起新 client。中途发现 client 被替换：还没写下任何东西就整体撤回；lease 交还已经落盘就按操作目标补齐焦点与打开集合（接口返回 409 与最新状态），由新 client 的重连通知收尾。
- deckd 跑在布局的后台窗口里：关掉布局 server（例如 `tmux -S … kill-server`）deckd 随之停止，agent 会话照常在 agent-deck server 上跑，
  这期间决定、通知、派送都不可用。再 `deck up` 会把 deckd 起来；接管中的会话按重启恢复处理（保持接管、自动派送暂停，等你交还）。
- 会话运行状态：n055 起 deckd 默认调 `agent-deck list --json` 读（不写会话状态；含已归档会话），不需要 agent-deck 的 headless 服务，
  没有 token、没有端口（owner 在设计复审选 A：经 TCP 端口发 token 无法保证对面是自己的服务，WSL2 mirrored 网络下尤甚）。
  间隔最少 3 秒、按上次耗时的 10 倍放大：每个会话（含已停的）约 6–7 ms，会话积累多了状态刷新会变慢，可用
  `agent-deck -p deck remove <id>` 清理不要的旧会话。读不到时圆点显示「未知」，原因在 `deck` 健康检查的 `activity_error` 里。
  仍可用 `deck up -upstream 127.0.0.1:8420`（或 `DECK_AGENTDECK_ADDR`）改连现成的 headless 服务，token 取 `WORKDECK_AGENTDECK_TOKEN`。

- **唯一写入者**：deck 的状态（注册表、决策、通知、Codex 授权记忆、汇报队列、lease）只由 deckd 写，放在 `$XDG_STATE_HOME/deck/`，与旧 `work` 的目录分开。
- **同一个 agent-deck profile 只许一个写入者**：deckd 与旧 `work` 都先取 `$XDG_STATE_HOME/agent-deck-writers/<profile>.lock`，取不到的一方只读。deck 默认用 profile `deck`。
- **按调用者分权限**：deckd 凭 `SO_PEERCRED` 与进程链认出调用者在哪个 tmux 窗格——布局窗格是 owner，注册表里的会话是主 agent 或子 agent，其他是未知（只能读额度、健康状态与 whoami 自身诊断，不能写）。项目与创建者一律由服务端推导。同一系统用户下这防的是模型越权与误操作，不防恶意代码。
- **需要 Linux 6.5 及以上**：连接身份经 `SO_PEERPIDFD` 绑定到对端进程本身，防进程号复用。内核不支持时，一切调用者按未知处理：只能读额度、健康状态与 whoami，不能写。
  （n050 另有一道墙钟出生时间检查，n049 去掉了：WSL 校时会移动 btime，同一个进程会被判成「连接后才出生」。）
- **预留名额（给 n051 的 `deck agent` 用，`deckstore.Store.Reserve`）**：
  - 主、子会话都必须带启动请求指纹：`request_id`（字母数字与 `._:-`，最长 64）、`dir`（规整的绝对路径，最长 300 字节）、`task_sha256` 与 `args_sha256`（64 位小写十六进制，分别是任务正文与实际启动参数的 sha256）。原样校验，不清洗，不合格就拒绝。
  - `request_id` 在整个注册表里只能用一次：不论记录现在是什么状态（已完成、失败，或结果不明的 uncertain）都占着它。重复请求返回错误，不是幂等地返回原预留；要重试就用新的请求 id。启动失败（`LaunchFailed`，非 uncertain）会释放名额；uncertain 不是终态，继续占名额，之后仍可能被确认为启动成功（`LaunchSucceeded`）。
  - 超过预算包络时要先建一个 cost 决定，scope 用 `SpawnScope(请求, 项目设置)`；owner 批准后 `BeginExecute`，再在有效期内以同一请求调 `Reserve(请求, 决定 id)`。目录、任务、参数、请求 id、项目目录、包络版本任一变了，例外都不能用；用一次即标为 applied。
  - 项目目录同样进 scope，所以设置里项目 `path` 最长 300 字节。
- **agent 设置**：`$XDG_CONFIG_HOME/deck/agents.yaml`（全局可用的 agent 与模型档位，首次运行生成默认值）、`projects.yaml`（每个项目选用哪些 agent、三档各用哪个模型、预算包络）、`panels.yaml`（面板开关）。
  - 两份文件每次保存写同一个 `generation`（上一代 +1），并把上一代留成 `.prev`。保存在两次写之间失败会留下一新一旧：读取时代数不一致就拒绝（deckd 不启动），按提示把较新的那份换回 `.prev`（没有 `.prev` 说明它是第一次写，删掉即回到默认），或核对后把两份的 generation 改成一样。第一次保存时 `projects.yaml` 还不存在，缺的那份按第 0 代算，同样能识别。手改时保持两份的 generation 一样即可；旧文件两份都没有这个字段，算同一代。
  - **升级前自查**：n049 起项目 `path` 要求规整的绝对路径、不超过 300 字节、不含换行与制表符等任何控制字符（项目目录会原样写进批准范围）。旧配置不满足时 deckd 启动失败并说明原因；先跑 `deck settings -check`，把超长或带控制字符的路径改掉再启动。
- **额度**：deckd 的额度门只读 Token 看板 `127.0.0.1:8899/api/data`；取不到、过期或没有窗口的来源按「未知」处理，不编数字。额度面板（`/v1/quota`）在此之外补几个只供显示的来源：Claude 读 owner 的 Claude Code 状态栏脚本缓存的官方用量（`/tmp/claude/<CLAUDE_CONFIG_DIR 目录名>/statusline-usage-cache.json`，超过一天或已过重置时刻的不用）；Antigravity 按看板的各额度池各一条；Kimi 暂时隐藏（`dashquota.DisplayTools`）。取不到的来源在面板上不显示。每个来源前有它的 logo，窗口后写重置时间（今天之内只写时刻）。OpenCode 免费模型没有限额接口，按看板的撞墙日均值算当天用量百分比（与看板 `calendar.js` 同口径：今天该模型的增量 + 缓存读 + reasoning ÷ 均值），标「日」，次日 0 点重置。
- 主 agent 工具（`deck agent`）在 n051。
- **额外参数**（`agents.yaml` 的 `extra_args`）：只能声明白名单里的参数（`agentdeck.declaredAllow`）。n055 起随启动真正传给 CLI
  （claude 经上游 `launch --extra-arg`，codex 经 `-c "codex 参数…"`），启动后照旧按白名单核对实际命令行。claude 的「参数=取值」写法
  取值带空白时要写成两个元素。
- **第一条消息前先看画面**（n055）：CLI 停在启动确认上就不送，记为等 owner 处理（侧栏与通知说明）；这期间不给它发话、不往主 agent
  推送汇报；确认消失、画面稳定后 deck 自动送一次。deckd 重启后，主 agent 接着等；子 agent 的任务正文没保存，提示 owner 重派。

## 测试

```bash
go test ./...
```

夹具在 `testdata/workdeck-fixture`，是一个七个节点分别停在七种状态的假项目，
账本由控制器亲自写出，和真项目同构。要重建它：

```bash
bash testdata/rebuild-fixture.sh
```
