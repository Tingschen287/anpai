# upstream agent-deck 依赖清单

Work Deck 对 upstream agent-deck 的依赖，按整理时找到的列在这里。上游升级时，拿这份清单逐条核对。

- 这里说的 **upstream agent-deck** 是开源项目 `github.com/asheshgoplani/agent-deck`，
  **不是**本仓自己那个同名的 Agent Deck 启动器。约定见项目的 `2.code/link.md`。
- 核对过的版本：**Agent Deck v1.15.0**，快照提交 `01c011b`。
- 机器可读的清单在 `manifest.go`（子命令白名单在 `client.go`）。两份不完全重合：
  本文件多写了几个参数和几个结构体的字段，那些没有机器可读的条目。
- 一致性测试在 `client_test.go`。各条测试核到哪一步，写在 `manifest.go` 头部注释里。
  它们不保证「清单外的东西一律会红」，例如拼出来的端点路径就看不见。
- 本文件只写现在成立的事。逐轮加固的过程与教训原样放在同目录 `MANIFEST-history.md`，那里的数字与行号是当时的。

## 1. 命令行子命令

| 子命令 | 用途 | 用到的参数 |
|---|---|---|
| `launch` | 派工：建会话、起会话（不带首条消息，见下文 n039） | `-c`（工具）、`--model`（模型 ID）、`--json`、`-t`（标题）、`-g`（分组）、`--no-wait` |
| `web` | 起 headless HTTP 服务 | `--no-tui`、`--listen`、`--token` |
| `session attach` | 进入会话，把终端交给真实 CLI | 只带会话号 |
| `session output` | 只读预览会话当前画面 | `-pane` |
| `session send` | 把任务文本（执行 brief、评审 brief）与决策队列的回答投进已登记写入权的会话 | `--message-file -`（文本走标准输入，不进 argv）、`--json`（n042，读失败时的 `delivery` 字段） |
| `list` | 直连 tmux 时取会话名，不经 headless 服务 | `--json` |
| `inbox export` | 决策队列：非消费式读全机 completion/transition 记录（n038） | `--json` |
| `inbox writer-status` | 决策队列：读 export 之前自检 notify-daemon 在不在（n038） | `--json` |

- 白名单在 `client.go` 的 `Subcommands`。清单之外的子命令，adapter 拒绝执行（`ErrSubcommandNotAllowed`）。
- **白名单精确到第二级。** 只写 `session` 会把 `session stop`、`remove` 等一并放行。
  `TestDangerousSessionSubcommandsAreRefused` 逐条确认 `session` 底下 8 个危险子命令被拒。
- `web` 在白名单里，但**发布代码不启动它**：Work Deck 连配置项 `upstream_addr` 指向的现成服务。
  只有集成测试用它起服务（`integration_test.go`），上表的参数就是测试用的那几个。
- `list` 直接读上游存储，不经 headless 服务。服务的菜单缓存看不见刚 launch 的会话，`list` 看得见（见第 7 节）。
  按 T 直连时先问 `/api/menu`，问不到才退到它。
- `session send` 用于派单的第二步：先起空会话、登记写入权（派评审还要核验上游返回的会话号），再投递 brief。
  n039 起执行者与评审两条入口都走它；空或只有空白的文本在派工器与 adapter 两层都拒绝。
  加它不放开 `session` 底下别的命令，二级匹配照旧生效。n038 起它也是决策队列回答的投递口。
- **n039 起 `SendMessage` 与 `DeliverAnswer` 必须带一张 `lease.Permit`**，在执行 `session send` 之前核对并登记「投递进行中」：
  写入权记账登记为 owner 接管着、等确认交还、租约过期、持有者未知时不投。许可只能由 `internal/lease` 签发，
  `TestEveryWriteIntoASessionNeedsAPermit` 核对包内往会话写字的上游调用只有 `SendMessage` 一处且先核许可。
  边界：记账只知道经 Work Deck 的接管；不经 Work Deck 的接入由 arbiter 在投递前用 `AttachedClients`（tmux `list-clients`）查，
  查询与送达之间有人进来仍然看不见（已披露的剩余风险，Graph revision 86）。
- **n042：`session send --json` 的 `delivery`**。失败时上游输出 `{"success":false,"delivery":...}`（上游 `cmd/agent-deck/session_cmd.go:3056-3125`）。
  `delivery=typed`（正文到了画面、提交没得到确认，issue #1793）报 `ErrDeliveryUnconfirmed`，派工器照样落绑定并记 `delivery: unconfirmed`；
  其余取值（`typed_not_submitted`、`line_too_long`、`no_evidence`、`send_failed`）仍是投递失败。`unverified` 上游按成功退出，Work Deck 照上游算成功。
  n040 实测 `typed` 出现过假阴性（评审会话其实执行了 brief）；n042 期间用 shell 会话试了 5 个 ASCII 单词没复现，所以这条由按上游 JSON 形状写的假上游测试覆盖。
- **n042：哪些工具接受 `--model`**（`ToolsAcceptingModel`，上游 `internal/session/instance.go:9318` `SupportsLaunchModel`）：claude、codex、gemini、opencode，
  以及用户在上游配置里声明为与 claude / codex 兼容的自定义工具。`shell` 没有模型（`ToolsWithoutModel`）。配置条目用 `model_not_applicable: true` 显式声明，
  `CheckToolModel` 在加载配置和发 launch 前核对两类已知冲突；认不出的自定义工具交给上游。升级上游要重核这张表。
- **n042：不继承 `TMUX` / `TMUX_PANE`**。tmux 不带 `-L`/`-S` 时按 `$TMUX` 找 server；Work Deck 自己跑在 tmux 里时，上游 launch、`has-session`、
  `show-environment`、`list-clients` 都会被带到外层 server（n040 终审实测：会话落到私有 server、随即 error）。所有上游与 tmux 子进程都用 `withoutTmux` 去掉这两个变量。
  依赖的约定：upstream agent-deck 的会话都在默认 tmux server 上。
- **n039 起不用 `launch -m`**：它在 Work Deck 登记写入权之前就把消息送进会话，登记失败时消息已经发出。
  现在一律先起空会话、登记写入权，再经 arbiter 的 `SendMessage` 投递。`LaunchRequest` 没有消息字段，`LaunchFlags` 不含 `-m`。
- **实测（n039）**：对 shell 会话，上游 `session send` 判「已提交」看正文是否还在输入区；shell 的
  `xxx: command not found` 回显会干扰这个判断，某些 ASCII 单词会被报成「reached the pane but submission was never
  confirmed」（issue #1793）而以退出码 1 失败。派评审时首条 brief 被这样误报，派工器会按「没送进去」拒绝并留下半成品会话。
- `inbox` 只放 `export` 与 `writer-status` 两条非消费式读取。`inbox drain`（conductor 的消费口，读完即删）
  与裸 `inbox <id>`（读完即截断）都拒绝，`TestInboxOnlyAllowsNonConsumingReads` 逐条确认。
  上游对 export 的承诺原文：「NON-DESTRUCTIVE ... two conductors draining the same host both get the records」
  （`internal/session/inbox_export.go:31-34`）。

### 白名单里哪些命令会写上游

**「在允许清单里」不等于「没有副作用」。** 这两件事分开记。

**先说一条对 inbox 之外各条命令都成立的**：这些子命令跑起来都要先加载 profile。
那条路（上游 `internal/session/storage.go:209` 的 `NewStorageWithProfile`）会
建 profile 目录、建 `state.db`、跑 schema 迁移，必要时还会把旧的 `sessions.json` 迁过来改名。
**所以它们没有一条是纯读的**，哪怕它叫 `list` 或 `output`。

`inbox` 两条例外：上游 main 在加载 profile 之前就把 `inbox` 分派出去了（`cmd/agent-deck/main.go:417`，
早于 `:518` 的 `NewStorageWithProfile`）。它们各自的副作用写在下表。

各条命令另外还写什么：

| 命令 | 除共有的 storage 初始化外（inbox 两条没有这一项），还写什么 | 上游依据（文件:行 · 所属函数） |
|---|---|---|
| `launch` | `InsertSessionAndVerify` 写进上游存储；`SetEnvironment` 写会话环境变量 `AGENTDECK_INSTANCE_ID` | `cmd/agent-deck/launch_cmd.go:626` · `handleLaunch`<br>`internal/session/instance.go:4939` · `Start` |
| `web` | 按配置跑 `costStore.PurgeOlderThan`，删超出保留期的成本记录 | `cmd/agent-deck/main.go:826` · `main` |
| `list` | **写 `state.db` 的 `last_activity_at`**（下面详述） | `cmd/agent-deck/main.go:2277` · `handleList` |
| `session attach` | 给目标 tmux 会话加一个 attached client；启动 badge watcher，`os.MkdirAll` 建 badge 更新目录 | `cmd/agent-deck/session_cmd.go:1370` · `handleSessionAttach`<br>`internal/tmux/badge_update.go:125` · `WatchBadgeUpdates` |
| `session output` | `-pane` 分支调 `PreviewFull`，最终由 `CaptureFullHistory` 跑 `capture-pane` 取画面 | `cmd/agent-deck/session_cmd.go:4409` · `handleSessionOutput`<br>`internal/tmux/tmux.go:3689` · `CaptureFullHistory` |
| `session send` | `executeSend` 把文本投进目标会话，对方当成输入处理 | `cmd/agent-deck/session_cmd.go:2894` · `handleSessionSend` |
| `inbox export` | 读三处记录取并集，不删不截断；但记录里出现的每个 profile 都会被 `NewStorageWithProfile` 打开，没有就建 `state.db` | `cmd/agent-deck/inbox_cmd.go:423` · `runInboxExport`<br>`internal/session/inbox_export.go:151` · `dropSuppressedChildren` |
| `inbox writer-status` | 只读 notify-daemon 的心跳文件 | `cmd/agent-deck/inbox_cmd.go:466` · `runInboxWriterStatus`<br>`internal/session/notify_heartbeat.go:74` · `ReadWriterStatus` |

`list --json` 在输出之前会先刷新状态，那条路一直通到 SQLite：

```
cmd/agent-deck/main.go:2277                     session.RefreshInstancesForCLIStatus(instances)
internal/session/cli_status_refresh.go:38       inst.UpdateHookStatus(hs)
internal/session/instance.go:6280               defer i.persistLastActivity(false)
internal/session/last_activity_persist.go:124   db.WriteLastActivityAt(...)
```

写的只是一个活动时间戳，不改会话状态、不停会话，但它是持久化写入。

### 这张表怎么被机器核对

上表在 `manifest.go` 里有机器可读的一份：`SideEffects` 与 `SharedStorageInit`。
那边不写「一条命令一段描述」，而是**一条断言一份依据**。

- `TestEveryAllowedSubcommandDeclaresItsSideEffects`：白名单每条命令都要有断言，且命令自己的依据不得等同于 `SharedStorageInit`。
- `TestSideEffectEvidencePointsAtRealUpstreamCode`：拿每条断言去上游快照的语法树里核对。

每条断言的 `Effect` 从封闭枚举 `EffectKind` 里挑一种：
`EffectCalls`（调用了某函数，可带一个常量参数）、`EffectDeclares`（声明了某函数）、
`EffectDeclaresConst`（声明了某常量，值为某）、`EffectAbsent`（在几个函数体里搜不到某些东西）。
人读的解释写在每条 `Claim` 上方的代码注释里，注释不参与核对。

依据分两种：

- **正面断言**用 `Evidence`：`Path` + `Line` + `Anchor`（那一行必须含的文本）+ `Within`（所属函数，按语法树判定）。
  锚点要落在被选中那个调用自己的源码区间里；同一行上有两个同名且都含锚点的调用时，测试拒绝。
  `Target` 写完整调用形式（`inst.PreviewFull`，不是 `PreviewFull`）。
- **否定断言**用 `Absence`：去 `In` 列出的函数体里搜，按常量折叠后的字符串搜，搜到就红。
  `NotCovered` 必须和这些函数体里的真实调用两头对上：编一个没有的会红，漏一个真实的会红，
  已经被 `In` 搜过的还写进来也会红。

核对有边界，测试注释与 `4.eval/results/n037_workbench_attach/boundary-limits.md` 第三节写明了：

- 从调用写法到接收者类型那一跳（`FuncRef.Note`），测试不验，是人读出来的。测试只解析上游源码，不做类型检查。
- `NotCovered` 列出的函数，不追进去搜。
- 锚点比对只剔注释，字符串字面量里的锚点仍参与匹配。

### 关于「session output 不发按键」

上游并非不发按键：快照 `01c011b` 的 `internal/tmux/tmux.go` 里有 8 处把 `send-keys` 或 `paste-buffer` 交给 tmux 的调用。

能证的只是一条有边界的否定断言：`session output -pane` 这条路上，`In` 列的 5 个函数体
（`handleSessionOutput`、`Instance.PreviewFull`、`Session.CaptureFullHistory`、`Session.runBoundedOutput`、包级 `runBoundedOutput`）
里搜不到这两个子命令。这 5 个函数体调用了、本证明不追进去的，`NotCovered` 列了 31 个。

这两个子命令的名字收在 `manifest.go` 的 `keyInjectionCommands` 这一个变量里。

## 2. HTTP 端点

服务地址来自配置 `upstream_addr`，带 bearer token（环境变量 `WORKDECK_AGENTDECK_TOKEN`，不落盘）。
集成测试确认无 token 时服务返回 **HTTP 401**。

| 端点 | 用途 | 说明 |
|---|---|---|
| `GET /api/sessions` | 冷启动取一次全量会话 | 返回 `sessions[]`、`groups[]`、`profile` |
| `GET /api/menu` | 按 T 直连 tmux 时取会话名与 socket 名 | 菜单快照的原始形态，含分组层级 |
| `GET /events/menu` | SSE 实时菜单 | 每次推**全量**快照，不是增量，收到整份替换 |

`TestEveryEndpointInSourceIsListed` 按字面量核对源码里的 `"/api/..."`、`"/events/..."` 都在清单里；拼出来的路径看不见。

## 3. 依赖的输出字段

`TestManifestMatchesTheStructsWeActuallyDecode` 只双向核对五组：`launch --json`、会话对象、菜单事件顶层、`inbox export` 记录、`inbox writer-status`。
其余几组（`list --json` 的行、分组、菜单行、`/api/sessions` 顶层）只写在这里，没有测试核对。

### `launch --json`（有测试核对）

`session_id`、`id`、`success`、`profile`、`tool`、`command`、`resolved_command`、
`title`、`group`、`path`、`model_id`、`model_version`、`error`、`code`

其中 `session_id` 是绑定的关键。上游只在模型 ID 非空时才带 `model_id` 与 `model_version`。

**注意**：上游派工失败时**退出码可能仍是 0**，真正的结论在 `success` 字段上，
原因在 `error` 与 `code` 里。只看退出码会把失败当成功。实测例：

```json
{"code":"INVALID_OPERATION","error":"model selection is not supported for tool \"shell\"","success":false}
```

并非所有工具都支持选模型（`shell` 就不支持）。adapter 要求调用方**显式声明**
`ModelNotApplicable`，不允许模型留空：「不知道用哪个模型」必须拒绝，「这个工具没有模型」是另一回事。

### 会话对象（`/api/sessions` 与菜单共用；有测试核对）

`id`、`title`、`tool`、`status`、`groupPath`、`projectPath`、`tmuxSession`、
**`tmuxSocketName`**、`command`、`createdAt`、`lastAccessedAt`

`tmuxSocketName` 在上游带 `omitempty`，默认 socket 时字段不出现，见第 5 节。

### 菜单事件顶层（有测试核对）

`profile`、`generatedAt`、`totalGroups`、`totalSessions`、`items`

### `inbox export --json` 的每条记录（有测试核对）

顶层是数组，没有记录时是 `[]`。字段照上游 `TransitionNotificationEvent`（`internal/session/transition_notifier.go:40-108`）：

`child_session_id`、`child_title`、`profile`、`from_status`、`to_status`、`timestamp`、`substate`、
`last_output_hash`、`target_session_id`、`target_kind`、`kind`、`done_status`、`done_summary`、
`turn_fingerprint`、`source_remote`、`dead_letter_reason`

- `kind` 为空是一次状态翻转；`kind` 为 `finished` 是完成回报，from/to 都是空串。
- 没有 id。去重键由 `InboxRecord.Fingerprint` 照上游 `EventFingerprint`（`internal/session/event_fingerprint.go:22-48`）重算，不含时间戳。
- 发起会话是 `child_session_id`；`target_session_id` 是它的父会话或 conductor，不是投递对象。

### `inbox writer-status --json`（有测试核对）

`running`、`last_heartbeat`、`age_seconds`、`detail`

上游只看心跳文件新旧（90 秒内算在跑，`internal/session/notify_heartbeat.go:31`），不论结果退出码都是 0，只能看 `running`。

### 没有测试核对的几组

| 来源 | 字段 | 代码里的结构体 |
|---|---|---|
| `list --json` 的行 | `id`、`title`、`tool`、`status`、`path`、`group`、`tmux_session`、`profile`、`archived` | `ListedSession`（`list.go`）；机器清单 `CLIJSONFields` 列了同一组，但没有测试比对 |
| 菜单行 `items[]` | `index`、`type`、`level`、`path`、`group`、`session` | `MenuItem`（`events.go`） |
| 分组 | `name`、`path`、`sessionCount` | `Group`（`sessions.go`） |
| `/api/sessions` 顶层 | `sessions`、`groups`、`profile` | `SessionList`（`sessions.go`） |

## 4. 环境变量

| 变量 | 用途 |
|---|---|
| `AGENTDECK_PROFILE` | 指定隔离 profile。**adapter 强制要求显式指定**，留空直接报错 |

留空会落到上游默认 profile。上游自己记录过一次事故（2025-12-11）：测试跑在真实 profile 上，
覆盖掉 36 个生产会话（上游 `internal/feedback/testmain_test.go:34` 的注释）。所以这里不接受默认值。

## 5. tmux 约定

### 会话名

- 形如 `agentdeck_<title>_<hash>`，由上游生成。
- Work Deck **只读**这个名字，从 `tmuxSession`（HTTP 会话对象）或 `tmux_session`（`list --json`）取，不自己按标题推算。

### socket 名：有，但默认值时看不见

| 来源 | 会话名 | socket 名 | 依据 |
|---|---|---|---|
| HTTP 会话对象（`/api/menu` 等） | `tmuxSession` | **`tmuxSocketName`** | 上游 `internal/web/session_data_service.go:88` |
| `list --json` 的行 | `tmux_session` | 无 | 上游 `cmd/agent-deck/main.go:2250` 的 `sessionJSON` 里没有这个字段 |

「无」这一格是读上游结构体定义得出的：`sessionJSON` 里没有 socket 字段。
这和 `omitempty` 造成的「有字段但这次为空」是两回事。

`tmuxSocketName` 是 `tmux -L` 的选择器，上游在会话创建时定下：
`--tmux-socket` 参数优先，其次 `config.toml` 的 `[tmux].socket_name`，都没有则为空（上游 `internal/session/instance.go:391`）。
**它带 `omitempty`。** 会话跑在默认 tmux server 上时值为空，整个字段不出现在 JSON 里。

用法：`tmuxSocketName` 非空就带 `-L <socket>`，为空就连默认 server。参数按 argv 逐个传，不拼 shell 字符串。

### socket 名拿不到时

按 T 走降级路径时，`list --json` 给得出 tmux 会话名，给不出 socket 名。**这时不能直接连。**
不同 tmux server 各有命名空间，同名会话可能毫不相干。不带 `-L` 会连进默认 server 上那个，而且不报错。

所以 `ResolveSocketFor` 扫一遍：`$TMUX_TMPDIR/tmux-<uid>`（未设 `TMUX_TMPDIR` 时 `/tmp/tmux-<uid>`）下每个 socket 探测一次。
每个 socket 上查两件事，**两件都对得上才算命中**：

1. 会话名精确匹配：用 tmux 的 `=<session>` 形式（`has-session`）。
2. 会话环境里的 `AGENTDECK_INSTANCE_ID` 等于上游给的 session id（`show-environment`）。

| 命中数 | 处置 |
|---:|---|
| 1 | 目标唯一，带上那个 socket 名去连 |
| 0 | 拒绝，并按原因分别报错（见下表） |
| ≥2 | 拒绝（`ErrAmbiguousTmuxTarget`） |

每个 socket 的探测结论（`socketscan.go`）：

| 探测结论 | 什么情况 | 拒绝时怎么说 |
|---|---|---|
| `probeNoSession` | tmux 明确答「没有这个会话」 | 会话可能已经结束了 |
| `probeDeadSocket` | socket 文件还在，server 已经没了 | 不提，这是常态 |
| `probeUnavailable` | 探测本身失败：tmux 不在、超时、权限、server 故障 | 查不出会话在不在，附上每个 socket 的原因 |
| `probeIdentityUnknown` | 名字在，读不到 `AGENTDECK_INSTANCE_ID` | 证明不了就是它，不连 |
| `probeIdentityMismatch` | 名字在，身份是别人 | 同名的是别人，并报出实际身份 |
| `probeMatch` | 名字在，身份也对 | 放行 |

判定 fail-closed：除 `probeMatch` 外一概不放行。每个探测有 2 秒上限（`probeTimeout`）。

**为什么要加等号**：`-t name` 是前缀匹配。只有 `name-suffix` 存在时，`-t name` 照样 exit 0。

```
tmux -L s has-session -t target            → exit 0   ← 只有 target-suffix 时也命中
tmux -L s has-session -t '=target'         → exit 1
tmux -L s has-session -t '=target-suffix'  → exit 0
```

**为什么还要比身份**：名字会撞，也会被前缀蒙混。`AGENTDECK_INSTANCE_ID` 是上游建会话时写进会话环境的
（第 1 节 `launch` 那行），和 session id 一对一。

`has-session` 与 `show-environment` 只查询。

n039 另加 `list-clients -t =<session> -F #{client_tty}`（`clients.go` 的 `AttachedClients`）：数一个会话连着几个 tmux 客户端，只查询，用来发现不经 Work Deck 的接管。曾各跑三次实测，`session_activity` 不变（前后都取到了值，不是空对空）。

### 嵌套

Work Deck 常开在 tmux 窗格里，进入会话就是两层。tmux 默认拒绝：

```
sessions should be nested with care, unset $TMUX to force
```

adapter 的 `prepareNested`（`nested.go`）在检测到自己处于 tmux 内时，把 `TMUX` 与 `TMUX_PANE`
从子进程环境中去掉，强制嵌套。捕获证据见交付物 `nested-tmux-capture.md`。

前缀键在嵌套下会被外层先截获，真人需要按两次。这一点在进入**之前**提示，因为进去之后看不到 Work Deck 的界面。

### 不发按键

进去之后是人在操作真实 CLI。Work Deck 包内不执行 `send-keys`。
这个词在本仓非测试 Go 源码里只出现在一处：`manifest.go` 的 `keyInjectionCommands`。
那是拿去上游函数体里搜的禁令列表（第 1 节），不是要跑的命令。

两条测试扫本包发布源码，各有边界：

- `TestAdapterMayAttachButNeverSendsKeysOrKills`：按语法树，含常量折叠，只放过 `keyInjectionCommands` 那一个声明。
- `TestPreviewPathIsReadOnly`：按文本逐行找，含 `keyInjectionCommands = ` 的整行跳过（已知缺口 K8）。

拦得住的写法由对照脚本 `scripts/positive-control.py` 实测；拦不住的写法列在脚本的 `GAPS` 与
`4.eval/results/n037_workbench_attach/boundary-limits.md`。

## 6. 明确不依赖的东西

| 不用 | 为什么 |
|---|---|
| `state.db` | 没有 hook overlay 时 `instances.status` 不权威，只可排障时看，不作真相来源 |
| 上游配置文件与 profile 目录内容 | Work Deck 只通过 `AGENTDECK_PROFILE` 指定名字，不读写其中文件 |
| 上游源码 | 不修改、不编译进自己的二进制，只调用命令与端点 |

## 7. 已知约束：CLI 派的会话，同一个服务实例看不到

**这是本清单里最要紧的一条。**

headless 服务对菜单快照做了内存缓存，缓存只在 `notifyMenuChanged`（上游 `internal/web/server.go:450`）被调用时失效。
调用它的有两类：服务自己的写接口（`handlers_sessions.go`、`handlers_groups.go` 等），
以及服务内的 hook 状态文件 watcher（`server.go:331-332`，某个会话的 hook 状态文件变了就触发）。
`launch` 是另一个进程，它本身不触发这两类，所以：

- 刚用 `launch` 派出去的会话，在**同一个**服务实例的 `/api/sessions` 与 `/events/menu` 里**看不到**，
  直到发生上面某一次失效，或服务重启。什么时候发生不由 Work Deck 控制，所以按「可能一直看不到」处理。
- 上游没有提供纯刷新端点；服务的写接口都带副作用。
- 数据确实落进了存储：**另起**一个服务实例就看得见。

上游自己的注释承认了这点，原文（`server.go`）：

> In headless (--no-tui) mode there is no TUI loop to call
> publishWebMenuSnapshot(), so without this the menu snapshot would remain
> frozen at its first-load state and new sessions created via the API would
> never appear until server restart.

它说的是 *via the API*，即经由服务自己的接口。经由 CLI 的，连这条路都没走上。

### Work Deck 对此的处置

1. 自己派出去的会话，**以绑定存储为准**，不等服务确认。`launch` 的返回值里已经有会话号、profile、工具和模型。
2. 界面上对服务尚未感知的会话据实标注。
3. `TestIntegrationLaunchLandsInStorageEvenThoughServerCachesIt` 在没有上述失效事件的条件下（新 profile、刚起的服务）
   把这条约束钉成了断言：上游哪天修好，该测试会失败，那时回来更新本节与视图逻辑。

## 7b. 已知约束：inbox 事件看得到什么、看不到什么（n038）

- **顶层会话的等待不进 export。** 没有父会话的会话，状态翻转按 `orphan` 丢弃，不写任何 inbox
  （`internal/session/inbox_outbox.go:467-471`、`transition_notifier.go:219-221`）。Work Deck 派出去的会话都是顶层的，
  所以它们的 running→waiting 不会出现；只有打出完成标记时，完成账本里会有一条 `finished`。
  能进 export 的等待来自有父会话（conductor 或上级会话）的子会话。
- **状态翻转只由 notify-daemon 写**（`cmd/agent-deck/inbox_cmd.go:447-450`）；完成账本另有 run-task 的 worker 会写
  （`internal/session/taskworker.go`）。daemon 不在时 export 里没有任何翻转，最多剩几条完成记录，
  和「没有待决事项」分不开，所以每次读之前先问 `writer-status`，并查进程表（心跳 90 秒内都算在跑）。
- **writer-status 在 daemon 刚退出的 90 秒里仍答 running**（只看心跳，`internal/session/notify_heartbeat.go:31`）。
  daemon 正常每 1–3 秒一轮、每轮开头写心跳（`internal/session/transition_daemon.go:20-22`、`:168-171`），
  所以 Work Deck 用自己的 15 秒门槛（`queue.HeartbeatFreshWithin`）判心跳新鲜，超过就告警。
  另读 `/proc/*/cmdline` 找「程序名含 agent-deck、参数有 notify-daemon」的进程（`NotifyDaemonRunning`），
  只作必要条件：本机一个都没有时一定告警。它分不出进程写的是哪个数据目录——要分得读别的进程的环境变量，
  那里可能有凭据，不读——所以不能单独当作在跑的证据。剩余窗口：本目录的 daemon 退出后最多 15 秒内，
  若本机另有 daemon 进程在跑，队列可能仍被当作可信。这不是上游命令调用，只读进程表。
- **export 是全机的，不分 profile。** Work Deck 只收本客户端 profile 的记录：别的 profile 的会话，`session send`
  在当前 profile 里找不到它，回答投递不过去。没收进来的条数在界面上说明。
- **父 inbox 会被 conductor 的 drain 清掉**，清掉后那些翻转就从 export 里消失；完成账本从不删除，`finished` 会一直在。
- **`events/` 目录在 v1.15.0 没有生产代码写入**：上游 `WriteStatusEvent`（`internal/session/event_writer.go`）
  只有测试在调，二进制里也没有相应字符串。按设计它是每会话一个文件、整体覆盖写，两次快速翻转会互相覆盖、丢中间态，
  本来就只能当「该刷新了」的信号，不能当队列。Work Deck 不读它，按固定间隔轮询 writer-status 与 export。

## 8. 上游变更时怎么核对

1. 跑 `go test ./internal/agentdeck/`（设 `WORKDECK_UPSTREAM_SRC` 指向新快照）。
   它能查出一部分不一致：白名单、五组字段、副作用断言的依据、源码里的字面量端点，
   以及端点、版本、提交、子命令、环境变量这几类在本文件出现。参数不查；字段只查 `tmuxSocketName` 一个
   （`TestManifestListsSocketName`，查词语出现与提到 `omitempty`），其余字段不查。
   查不出的部分见 `manifest.go` 头部与 `boundary-limits.md` 第三节，要靠下面几步人工核对。
2. 对照第 1、2 节，确认子命令与端点还在、参数名没改。
3. 对照第 3 节，确认字段名没改、语义没变（尤其 `session_id` 与 `status`）。没有测试核对的那几组要逐个看。
4. 第 4 节的环境变量若改名，adapter 的 profile 隔离会失效，**这是最危险的一条**。
