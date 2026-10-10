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
./deck daemon                  # 后台服务 deckd：$XDG_STATE_HOME/deck/deckd.sock（0600）
./deck panel quota             # 在当前窗格跑一块面板：decisions、quota、progress、notifications、activity
./deck whoami                  # deckd 把当前进程认成谁
```

- **唯一写入者**：deck 的状态（注册表、决策、通知、Codex 授权记忆、汇报队列、lease）只由 deckd 写，放在 `$XDG_STATE_HOME/deck/`，与旧 `work` 的目录分开。
- **同一个 agent-deck profile 只许一个写入者**：deckd 与旧 `work` 都先取 `$XDG_STATE_HOME/agent-deck-writers/<profile>.lock`，取不到的一方只读。deck 默认用 profile `deck`。
- **按调用者分权限**：deckd 凭 `SO_PEERCRED` 与进程链认出调用者在哪个 tmux 窗格——布局窗格是 owner，注册表里的会话是主 agent 或子 agent，其他是未知（只能读额度与健康状态）。项目与创建者一律由服务端推导。同一系统用户下这防的是模型越权与误操作，不防恶意代码。
- **需要 Linux 6.5 及以上**：连接身份经 `SO_PEERPIDFD` 绑定到对端进程本身，防进程号复用。内核不支持时，一切调用者按未知处理：只能读额度与健康状态，不能写。
- **预留名额（给 n051 的 `deck agent` 用，`deckstore.Store.Reserve`）**：
  - 主、子会话都必须带启动请求指纹：`request_id`（字母数字与 `._:-`，最长 64）、`dir`（规整的绝对路径，最长 300 字节）、`task_sha256` 与 `args_sha256`（64 位小写十六进制，分别是任务正文与实际启动参数的 sha256）。原样校验，不清洗，不合格就拒绝。
  - `request_id` 在整个注册表里只能用一次，失败、uncertain 等终态记录也占着它。重复请求返回错误，不是幂等地返回原预留；要重试就用新的请求 id。启动失败（`LaunchFailed`，非 uncertain）会释放名额，uncertain 继续占名额。
  - 超过预算包络时要先建一个 cost 决定，scope 用 `SpawnScope(请求, 项目设置)`；owner 批准后 `BeginExecute`，再在有效期内以同一请求调 `Reserve(请求, 决定 id)`。目录、任务、参数、请求 id、项目目录、包络版本任一变了，例外都不能用；用一次即标为 applied。
  - 项目目录同样进 scope，所以设置里项目 `path` 最长 300 字节。
- **agent 设置**：`$XDG_CONFIG_HOME/deck/agents.yaml`（全局可用的 agent 与模型档位，首次运行生成默认值）、`projects.yaml`（每个项目选用哪些 agent、三档各用哪个模型、预算包络）、`panels.yaml`（面板开关）。
- **额度**：只读 Token 看板 `127.0.0.1:8899/api/data`；取不到、过期或没有窗口时显示「未知」与原因，不编数字。
- 布局（`deck up`、侧栏、中间窗格）在 n049，主 agent 工具（`deck agent`）在 n051。

## 测试

```bash
go test ./...
```

夹具在 `testdata/workdeck-fixture`，是一个七个节点分别停在七种状态的假项目，
账本由控制器亲自写出，和真项目同构。要重建它：

```bash
bash testdata/rebuild-fixture.sh
```
