> **不是现行陈述。** 本文件是 n037 attempt 25 时 `MANIFEST.md` 的原样副本（commit `aa079de`），
> 保留逐轮加固过程与教训。里面的数字、行号、节号与「这一轮/上一轮」都是当时的，很多已经过时。
> 现行依赖清单以同目录 `MANIFEST.md` 为准。

# upstream agent-deck 依赖清单

Work Deck 对 upstream agent-deck 的全部依赖列在这里。上游升级时，拿这份清单逐条核对。

- 这里说的 **upstream agent-deck** 是开源项目 `github.com/asheshgoplani/agent-deck`，
  **不是**本仓自己那个同名的 Agent Deck 启动器。约定见项目的 `2.code/link.md`。
- 核对过的版本：**Agent Deck v1.15.0**，快照提交 `01c011b`
- 机器可读的同一份清单在 `manifest.go`。一致性测试在 `client_test.go`，各自核到哪一步写在 `manifest.go` 头部注释里——
  不是「清单外的东西一律会红」，例如拼出来的端点路径就看不见

## 1. 命令行子命令

| 子命令 | 用途 | 用到的参数 |
|---|---|---|
| `launch` | 派工：建会话、起会话、可选发首条消息 | `-c`（工具）、`--model`（模型 ID）、`--json`、`-t`（标题）、`-g`（分组）、`-m`（首条消息）、`--no-wait` |
| `web` | 起 headless HTTP 服务 | `--no-tui`、`--listen`、`--token` |
| `session attach` | 进入会话，把终端交给真实 CLI | 只带会话号 |
| `session output` | 只读预览会话当前画面 | `-pane` |
| `session send` | 把评审 brief 投进一个已核验会话号的评审会话 | `--message-file -`（文本走标准输入，不进 argv） |
| `list` | 直连 tmux 时取会话名，不经 headless 服务 | `--json`（**有写副作用**，见下） |

清单之外的子命令，adapter 会直接拒绝执行（`ErrSubcommandNotAllowed`）。

`list` 是 attempt 7 加的。它直接读上游存储，不经 headless 服务——
这正是它的用处：服务的菜单缓存看不见刚 launch 的会话，而 `list` 看得见。
按 T 直连时先问 `/api/menu`，问不到就退到它。实测见第 7 节末尾。

### 白名单里哪些命令会写上游

**「在允许清单里」不等于「没有副作用」。** 这两件事分开记：

**先说一条对所有命令都成立的**：每条 CLI 子命令跑起来都要先加载 profile，
那条路（上游 `internal/session/storage.go:209` 的 `NewStorageWithProfile`）会
建 profile 目录、建 `state.db`、跑 schema 迁移，必要时还会把旧的 `sessions.json`
迁过来改名。**所以没有一条命令是纯读的**，哪怕它叫 `list` 或 `output`。

各条命令另外还写什么：

| 命令 | 除共有的 storage 初始化外，还写什么 | 上游依据（文件:行 · 所属函数） |
|---|---|---|
| `launch` | `InsertSessionAndVerify` 写进上游存储；`SetEnvironment` 写会话环境变量 | `cmd/agent-deck/launch_cmd.go:626` · `handleLaunch`<br>`internal/session/instance.go:4939` · `Start` |
| `web` | 按配置跑 `costStore.PurgeOlderThan`，删超出保留期的成本记录 | `cmd/agent-deck/main.go:826` · `main` |
| `list` | **写 `state.db` 的 `last_activity_at`**（下面详述） | `cmd/agent-deck/main.go:2277` · `handleList` 等三处 |
| `session attach` | 启动 badge watcher，`os.MkdirAll` 建 badge 更新目录；给目标 tmux 会话加一个 attached client | `internal/tmux/badge_update.go:125` · `WatchBadgeUpdates` |
| `session output` | `-pane` 分支调 `PreviewFull`，最终由 `CaptureFullHistory` 跑 `capture-pane`；只读画面 | `cmd/agent-deck/session_cmd.go:4409` · `handleSessionOutput`<br>`internal/tmux/tmux.go:3689` · `CaptureFullHistory` |
| `session send` | `executeSend` 把文本投进目标会话，对方当成输入处理 | `cmd/agent-deck/session_cmd.go:2894` · `handleSessionSend` |

这张表在 `manifest.go` 里有机器可读的同一份（`SideEffects` 与 `SharedStorageInit`）。
那边不写成「一条命令一段描述」，而是**一条断言一份依据**，各自核对。

每条断言有两个字段，**两个都由机器核对**：

| 字段 | 是什么 | 测试怎么验 |
|---|---|---|
| `Effect` | 上游做的那件事，从一个**封闭枚举**里挑一种，加一个名字 | 拿它去上游**语法树**里查证，并且**锚点必须落在被选中那个调用自己的源码区间里** |
| `Evidence` | 那件事在源码里的落点，供人翻阅 | 锚点必须在指定行上；所属函数按语法树的位置区间判定 |

**没有第三个字段。** 人读的解释写在每条 `Claim` 上方的代码注释里——
注释是注释，不会被当成有依据的断言。

`EffectKind` 目前有四种：`EffectCalls`（调用了某函数，可带一个常量参数）、
`EffectDeclares`（上游声明了某函数）、`EffectDeclaresConst`（声明了某常量，值为某）、
`EffectAbsent`（在几个函数体里搜不到某些东西）。

**枚举加钉行，两个一起才管用。** attempt 13 只做了枚举，论据是
「想说一件这里没有的事，连个 `Kind` 都挑不出来」。独立评审一行就破了它：
把 `session attach` 的 `Target` 从 `tmuxSession.Attach` 换成同一个函数错误分支里的
`os.Exit`，`Anchor` 原封不动指着 `Attach` 那一行——**通过**。
因为 `handleSessionAttach` 里确实有 `os.Exit`。

**你不用编，在同一个函数里挑另一件真实但无关的事就行。**

所以 attempt 14 把断言钉到了它自己的依据那一行。**但那还不够。**
评审下一轮指出：同一行可以有好几个调用——

```go
outer(inner())
```

两个都起始于这一行，于是 `Anchor` 描述 `outer`、`Target` 写 `inner`，照样通过。
评审实测更直接：在依据那一行里嵌一个 `os.Exit()`，把 `Target` 改成 `os.Exit`，**通过**。

「同一行」是共线关系，不是身份关系。所以 attempt 16 再收一层：
**锚点必须出现在被选中那个 `CallExpr` 自己的源码文本里**。
同一行上有两个都叫这个名字、且都含锚点的调用时，测试直接拒绝，要求写得更具体。
`Target` 也改成写完整形式（`inst.PreviewFull` 而不是 `PreviewFull`），
因为 `safe.Foo` 和 `evil.Foo` 得分得开。

依据本身分两种：

- **正面断言**用 `Evidence`：`Path` + `Line` + `Anchor`（那一行必须含的文本）
  + `Within`（那一行所在的函数，按语法树判定，留空表示文件顶层且会被核对）。
- **否定断言**用 `Absence`：去指定的几个**函数体**里搜，搜到就红。
  搜的是**折叠成常量之后**的字符串，所以 `"send" + "-keys"` 也算数。
  必须写 `NotCovered`：逐个列出这几个函数体里调用了、而本证明追不进去的函数名。
  编一个语法树里没有的会红，漏掉一个真实调用也会红，
  **已经被 `In` 搜过的还写进来也会红**——不能既说搜过又说没追进去。

  `In` 也不能随便挑，而且**覆盖资格按调用写法算，不按尾名**。
  attempt 15 这里按尾名算，评审往上游副本加了个 `(*Other).PreviewFull`，
  把 `In` 里的 `Instance.PreviewFull` 换成 `Other.PreviewFull`——
  真正的那个一字节没被搜，测试照样通过。

  所以 `FuncRef` 现在有四个字段：`Func`（定义处身份）、`Call`（上一级里的调用写法）、
  `Note`（那个变量凭什么是这个类型）、`Path`。机器核对 `Call` 真的在上一级的调用
  列表里、`Func` 在 `Path` 里唯一存在；**而从变量到类型那一跳，`Note` 写明是人读的**。
  测试只解析上游源码不编译它，没有类型信息——验不了的就写出来，跟 `NotCovered` 一个道理。

  **这不是原则上做不到**：`go/types` 能验出不少接收者类型。不做是因为要把上游
  连同全部依赖加载并类型检查，慢且脆——依赖拉不动时测试会红，而红的原因
  跟它要守的事无关。代价写在 `FuncRef` 的注释里，哪天上游快照能稳定类型检查了，
  这是第一个该收回去的边界。

  同一条命令里别的断言说「调用了 X」，而 X 真的在这条路上，
  那 X 就必须进 `In` 被实际搜过。attempt 13 的 `In` 只有头尾两个函数，
  中间的 `Instance.PreviewFull` 写进 `NotCovered` 就算交代了；
  独立评审复制上游文件，在 `PreviewFull` 里加一行 `_ = "send-keys"`，**测试照样通过**。

再加一条：**命令自己的依据不得等同于 `SharedStorageInit` 那条。**

> 这个形状是被六轮独立评审逼出来的，每一次加固都对应一次它曾经放过的假依据：
>
> - **attempt 9**：只写 `文件:行`，只验「行号没超出文件总行数」。
>   行号改成同文件任意一行，照样通过。
> - **attempt 10**：补了 `Anchor`，但锚点可以跟声明毫不相干——
>   `session output` 声称「跑 `capture-pane` 不发按键」，依据却指着
>   `NewStorageWithProfile`。
> - **attempt 11**：加了 `Within` 与 What 核对，但 What 是一整段自由文本，
>   **一个 token 命中就整条放行**。评审举的反例：
>   `What: "删除整个用户数据库，然后调用 PreviewFull 读取画面"`——
>   `PreviewFull` 命中，前半句一路绿灯。
> - **attempt 12**：拆成 `Does`/`Means`，要求 `Does` 逐字出现在自己的锚点上。
>   评审当场实测反例：`Does` 从锚点抄一段，`Means` 写「删除整个用户数据库，
>   并把所有密码发到公网」，**通过**。通行证从「一个命中的 token」
>   变成了「抄一段 Anchor」，病没变。
> - **attempt 13**：不再假装机器能核对自然语言，把断言换成封闭枚举，
>   全部核对改走 `go/ast`。同一轮评审还给了两个绕过法，也一并封了：
>   原始字符串里放一行顶格的 `func Fake() {` 让按文本切的函数体提前结束（漏报），
>   以及把禁词写成 `"send" + "-keys"` 让按文本扫的守边测试看不见。
> - **attempt 14**：枚举不等于绑定。`Target` 换成同函数里另一个真实调用照样通过；
>   `In` 由人手挑，漏了真实调用链上的中间函数；守边能被
>   `string([]byte{115,101,...})` 绕过。前两条各加一条硬规则，
>   第三条改盯出口——**算出来的字符串不许成为跑出去的命令参数**。
> - **attempt 17**：命令来源那条保证实际只在同一个函数内成立——包一层
>   `func wrap(cmd string) { exec.Command(cmd) }` 就绕过了；锚点按源码子串比对，
>   在另一个同名调用里写段注释抄一份锚点就能冒充。
>   前者改成自动发现转发参数的 wrapper 并迭代到不动点，且按 argv 位置分开检查
>   （跟在字面量 flag 后面的是数据位，tmux 不会把它当子命令）；
>   后者剔除注释再比对。阳性对照脚本也进了仓，评审可以自己跑。
> - **attempt 16**：三条线还是虚的。钉到「同一行」挡不住同行的第二个调用；
>   覆盖资格按尾名算，随便一个同名方法就能顶替；命令参数只追一层，
>   `evil := string([]byte{…})` 再 `append` 进去就绕过了。
>   分别收成：锚点落在节点区间内、覆盖按调用写法算并把类型那一跳标成人读、
>   数据流迭代到不动点且标识符与字段读取都不再无条件放行。
>
> 教训：**「有依据」和「依据被真正核对过」，是两回事。**
> 而反复被绕过之后要看的不是「检查够不够严」，而是
> **「这个字段机器到底核不核得了」**——核不了的，加多少层匹配都是装饰。

**关于「不发按键」这句话。** 它曾经是无依据的——上游
`internal/tmux/tmux.go` 里有 21 处调用注入按键的子命令，所以
「上游不发按键」本来就是假话。现在它是一条有边界的否定断言：
只证明 `handleSessionOutput` 与 `CaptureFullHistory` **这两个函数体**里没有，
不覆盖它们调用的其他函数。

**那个边界有多窄，`NotCovered` 里数得出来：二十七个调用没追进去。**
这个数字自己会说话，比一句「覆盖范围有限」诚实。

那些子命令的名字收在 `keyInjectionCommands` 这一个变量里，
守边测试放过的是**语法树上的这一个声明**，不是「哪一行文本里有这个名字」——
上一轮按行文本跳过，另起一个名字含这个子串的变量就能蹭过去。


`list --json` 在输出之前会先跑一遍状态刷新，那条路一直通到 SQLite：

```
cmd/agent-deck/main.go:2277           RefreshInstancesForCLIStatus(instances)
internal/session/cli_status_refresh.go:37    inst.UpdateHookStatus(hs)
internal/session/instance.go:6280            defer i.persistLastActivity(false)
internal/session/last_activity_persist.go:124   db.WriteLastActivityAt(...)
```

写的只是一个活动时间戳，不改会话状态、不停会话。但它确实是持久化写入，
所以 attempt 7 说的「白名单只新增了一个只读命令」不成立。

> 这条是独立评审顺着上游源码追出来的。执行者读过 `list --json` 那段代码——
> `RefreshInstancesForCLIStatus` 就在他查的 `sessionJSON` 结构体下面几行——
> 却没追进去看它做什么，就写下了「只读」。
>
> 紧接着的一轮又错了一次：把 `session attach` / `session output` 硬编码成
> 「不写」，而它们同样走 `NewStorageWithProfile`，`attach` 还会 `MkdirAll`
> 一个 badge 目录。当时的测试只验证「每条命令被放进某个桶」，
> **验证不了桶里的描述是不是真的**，所以照样全绿。
>
> 教训两条：
> 1. **一个命令叫什么名字，不说明它做了什么。** 加进白名单之前追到底。
> 2. **声明要有据可查。** 现在每条副作用都带上游源码位置，测试会去核对。

机器可读的同一份声明在 `manifest.go` 的 `SideEffects`。`TestEveryAllowedSubcommandDeclaresItsSideEffects`
要求白名单每条命令都有声明，`TestSideEffectEvidencePointsAtRealUpstreamCode` 拿依据去上游快照核对；
后者核到哪一步是有边界的（`NotCovered` 不追进去，锚点比对只剔注释），见其注释。

`session send` 是 n037 attempt 6 加的。派评审必须分两步——先起空会话、
核验上游返回的会话号、再投递 brief——否则 brief 随 launch 出去，
万一撞上执行者的会话就撤不回来。加这一条**不会**放开 session 底下别的命令，
下面那段说的整条二级匹配照旧生效。

> 执行者在 attempt 5 曾以「本包没有向已有会话发消息的能力，加它等于放开
> 一整片操作面」为由拒绝实现两阶段投递。两个前提都是错的：上游有
> `session send`（`cmd/agent-deck/session_cmd.go:2685`），而白名单本来就是
> 整条二级路径比对。独立评审指出了这一点。
>
> 教训与第 7 节那次同类：**断言某个上游能力不存在之前，先去它源码里找一遍。**

**白名单精确到第二级。** 只写 `session` 会把 `session stop` / `remove` / `restart` /
`archive` / `fork` / `set` 一并放行，而 Work Deck 没有杀掉或改掉别人会话的道理。
`TestDangerousSessionSubcommandsAreRefused` 逐条盯着这些命令被拒。

## 2. HTTP 端点

服务地址固定为回环地址，带 bearer token。实测无 token 返回 **HTTP 401**。

| 端点 | 用途 | 说明 |
|---|---|---|
| `GET /api/sessions` | 冷启动取一次全量会话 | 返回 `sessions[]`、`groups[]`、`profile` |
| `GET /api/menu` | 按 T 直连 tmux 时取会话名与 socket 名 | 菜单快照的原始形态，含分组层级 |
| `GET /events/menu` | SSE 实时菜单 | 每次推**全量**快照，不是增量，收到整份替换 |

`GET /api/menu` 在 attempt 6 之前一直没进这张表，尽管生产路径已经在调它。
漏得掉的原因是一致性测试只做单向检查——「清单列的端点能在源码里找到」，
不检查「源码调的端点都在清单里」。attempt 7 补了反向那一半
（`TestEveryEndpointInSourceIsListed`）。

## 3. 依赖的输出字段

### `launch --json`

`session_id`、`id`、`success`、`profile`、`tool`、`command`、`resolved_command`、
`title`、`group`、`path`、`model_id`、`model_version`、`error`、`code`

其中 `session_id` 是绑定的关键。上游只在模型 ID 非空时才带 `model_id` 与 `model_version`。

**注意**：上游派工失败时**退出码可能仍是 0**，真正的结论在 `success` 字段上，
原因在 `error` 与 `code` 里。只看退出码会把失败当成功。实测例：

```json
{"code":"INVALID_OPERATION","error":"model selection is not supported for tool \"shell\"","success":false}
```

并非所有工具都支持选模型（`shell` 就不支持）。adapter 对此要求调用方**显式声明**
`ModelNotApplicable`，而不是允许模型留空——「不知道用哪个模型」必须拒绝，
「这个工具没有模型」是另一回事。

### 会话对象（`/api/sessions` 与菜单事件共用）

`id`、`title`、`tool`、`status`、`groupPath`、`projectPath`、`tmuxSession`、
**`tmuxSocketName`**、`command`、`createdAt`、`lastAccessedAt`

`tmuxSocketName` 带 `omitempty`，默认 socket 时字段不出现，见第 7 节。
这份清单此前漏了它——漏的原因和第 7 节记的是同一次误判。

### 菜单事件顶层

`profile`、`generatedAt`、`totalGroups`、`totalSessions`、`items`

## 4. 环境变量

| 变量 | 用途 |
|---|---|
| `AGENTDECK_PROFILE` | 指定隔离 profile。**adapter 强制要求显式指定**，留空直接报错 |

留空会落到上游默认 profile。上游自己记录过一次事故（2025-12-11）：测试跑在真实
profile 上，一次覆盖掉 36 个生产会话。所以这里不接受默认值。

## 5. tmux 约定

### 会话名

- 形如 `agentdeck_<title>_<hash>`，由上游生成
- Work Deck **只读**这个名字，从 `tmuxSession`（`/api/menu`）或 `tmux_session`
  （`list --json`）字段取，不自己按标题推算

### socket 名：有，但默认值时看不见

| 来源 | 会话名 | socket 名 | 依据 |
|---|---|---|---|
| `GET /api/menu` 的 session 对象 | `tmuxSession` | **`tmuxSocketName`** | `internal/web/session_data_service.go:88` |
| `list --json` 的行 | `tmux_session` | 无 | `cmd/agent-deck/main.go:2250` 的 `sessionJSON` 里没有这个字段 |

「无」这一格是读上游结构体定义得出的，不是「跑了一次没看见」——
`sessionJSON` 整个结构体里就没有 socket 字段，所以它不可能出现，
这和 `omitempty` 造成的「有字段但这次为空」是两回事。

`tmuxSocketName` 是 `tmux -L` 的选择器，上游在会话创建时定下
（`--tmux-socket` 参数优先，其次 `config.toml` 的 `[tmux].socket_name`，都没有则为空）。

**它带 `omitempty`。** 会话跑在默认 tmux server 上时值为空，整个字段不出现在 JSON 里。

> 本包此前在这里错过一次：只探测了一个用默认 socket 的会话，没看到字段，
> 就断言「两个接口都不暴露 socket 名」，并据此实现了一套探测默认 server 的绕行方案。
> 独立评审读源码发现了 `omitempty`，随后用 `--tmux-socket` 造一个非默认 socket 的
> 会话做阳性对照，字段立刻出现。
>
> 教训：**字段没出现，可能是它为空，不代表它不存在。** 判断「某个东西不存在」
> 之前，先造一个它应该存在的场景看看。

用法：`tmuxSocketName` 非空就带 `-L <socket>`，为空就是默认 server，不带。
参数按 argv 逐个传，不拼 shell 字符串——socket 名来自上游数据。

#### socket 名拿不到时怎么办

按 T 走降级路径时，`list --json` 给得出 tmux 会话名，给不出 socket 名。
**这时不能就这么连。**

tmux 的不同 server 各有各的命名空间，同名会话完全可能是毫不相干的两个东西。
不带 `-L` 连上的是默认 server 上那个——而且不会报错，人会以为自己进对了地方。
**连错比连不上严重得多。**

所以扫一遍：`$TMUX_TMPDIR`（没设就是 `/tmp/tmux-<uid>`）下每个 socket 跑一次
`has-session`，看这个会话名出现在几个 server 上。

每个 socket 上查两件事，**两件都对得上才算命中**：

1. 会话名精确匹配 —— 用 tmux 的 `=<session>` 形式
2. 会话里的 `AGENTDECK_INSTANCE_ID` 等于上游给的 session id

| 命中数 | 处置 |
|---:|---|
| 1 | 目标唯一，把那个 socket 名带上去连（顺便把 socket 名找回来了） |
| 0 | 拒绝，并说清是哪一种（见下表） |
| ≥2 | 说不清是哪个，**拒绝**（`ErrAmbiguousTmuxTarget`） |

**一个都没对上时，三种情形分开说。** 放行与否都一样是拒绝，
但人该去查的东西完全不同，所以不能都说成「找不到」：

| 探测结论 | 什么情况 | 拒绝时怎么说 | 人该做什么 |
|---|---|---|---|
| `probeNoSession` | tmux **明确答**「没有这个会话」 | 会话可能已经结束了 | 去看会话是不是收了 |
| `probeDeadSocket` | socket 文件还在，server 已经没了 | 不说——这是常态 | 什么都不用做 |
| `probeUnavailable` | 探测本身失败：tmux 不在、超时、权限、server 故障 | **查不出会话在不在**，附上每个 socket 的原因 | 先把探测修好，别去查会话 |
| `probeIdentityUnknown` | 名字在，读不到 `AGENTDECK_INSTANCE_ID` | 证明不了就是它，宁可不连 | 看那个 server 是否还响应；或这会话不是上游建的 |
| `probeIdentityMismatch` | 名字在，身份是别人 | 同名的是别人，**并报出实际身份** | **千万别连**，那是另一个会话 |
| `probeMatch` | 名字在，身份也对 | 放行 | — |

判定始终 fail-closed：除 `probeMatch` 外一概不放行。分这么细只为一件事——
**说得清**。

> 这里也改过两轮。第一版把几种情形一律折叠成 `bool`，界面只能笼统说「找不到」。
> 第二版分了身份那三种，却把「探测失败」并进了「没有这个会话」：
> 所有 socket 都探测不动时，界面照样讲「它可能已经结束了」。
>
> **那是一句没有依据的断言。** 一次都没问成，凭什么说会话不在了。
> 把「server 不响应」说成「会话已结束」，人会照着去查一个根本没发生的问题。
>
> 死 socket 单列一类，是因为目录里留着没有 server 的 socket 文件属于常态。
> 把它们算进「探测失败」，每次拒绝都会附一长串噪音，真正的原因反而被埋掉。

**为什么要加等号**：`-t name` 是前缀匹配。只有 `name-suffix` 存在时，
`-t name` 照样 exit 0，随后 attach 进去的就是那个后缀会话。

```
tmux -L s has-session -t target            → exit 0   ← 只有 target-suffix 时也命中
tmux -L s has-session -t '=target'         → exit 1
tmux -L s has-session -t '=target-suffix'  → exit 0
```

**为什么还要比身份**：名字会撞，也会被前缀蒙混；`AGENTDECK_INSTANCE_ID`
是上游建会话时写进会话环境的，和 session id 一对一。它还顺带盖住了
「扫描到 attach 之间同名会话被换掉」这个竞态。

`has-session` 与 `show-environment` 都只查询，不改会话状态——各跑三次实测，
`session_activity` 不变（前后两次都取到了值，不是空对空）。
死 socket 文件会报 "no server running" 并以非零码返回，自然被跳过。
每个探测有 2 秒上限，僵死的 server 不会把界面卡住。

> attempt 7 的降级路径是无条件按默认 server 连，还在提示里说「非默认 socket
> 就会连不上」。独立评审实测复现：在两个 server 上建同名会话，
> 不带 `-L` 会**静默连进默认 server 上那个**——不是连不上，是连错了。
>
> 教训：**「通常不会同名」不是可以拿来赌的前提。** 证明不了目标唯一就别连。
>
> 下一轮又错了一次：扫描用的是 `-t name`，而那是前缀匹配。
> 目标已结束、只剩一个同前缀会话时，扫描会判「唯一」然后连错。
> 现在名字用精确形式，并且加了身份比对。
>
> 教训之二：**一个命令的匹配语义，不看文档就不知道。** `-t` 看着像精确匹配。

## 嵌套

Work Deck 常开在 tmux 窗格里，进入会话就是两层。tmux 默认拒绝：

```
sessions should be nested with care, unset $TMUX to force
```

adapter 的 `prepareNested` 在检测到自己处于 tmux 内时，把 `TMUX` 与 `TMUX_PANE`
从子进程环境中去掉，强制嵌套。退出内层后回到外层，外层会话不受影响。
捕获证据见交付物 `nested-tmux-capture.md`。

前缀键在嵌套下会被外层先截获，真人需要按两次。这一点在进入**之前**提示，
因为进去之后就看不到 Work Deck 的界面了。

## 5b. 不发按键

进去之后是人在操作真实 CLI。Work Deck 不替他敲任何键：包内不执行 `send-keys`。
这个词在发布代码里只出现在一处——`manifest.go` 的禁令列表 `keyInjectionCommands`，
那是拿去上游函数体里搜、证明 session output 不碰它的否定断言，不是要跑的命令。
测试 `TestAdapterMayAttachButNeverSendsKeysOrKills` 盯着这条，并且只放过那一个声明。

它盯得住的是对照脚本列出的写法（字面量、拼接、可折叠常量、算出来的字符串经
本包函数与局部变量中转等）；盯不住的写法列在脚本的 GAPS 里，
边界与人工审计见 `4.eval/results/n037_workbench_attach/boundary-limits.md`。


- 会话名形如 `agentdeck_<title>_<hash>`，由上游生成
- Work Deck **只读**这个名字，从 `tmuxSession` 字段取，不自己按标题推算
- 不发按键（包内不执行 `send-keys`，它只出现在禁令列表里；`TestAdapterMayAttachButNeverSendsKeysOrKills` 在有限边界内盯着，见 5b）
- attach 自 n037 起已经在用，这一行原先写着「本轮不 attach」，那是 n036 时的话

## 6. 明确不依赖的东西

| 不用 | 为什么 |
|---|---|
| `state.db` | 没有 hook overlay 时 `instances.status` 不权威，只可排障时看，不作真相来源 |
| 上游配置文件与 profile 目录内容 | Work Deck 只通过 `AGENTDECK_PROFILE` 指定名字，不读写其中文件 |
| 上游源码 | 不修改、不编译进自己的二进制，只调用命令与端点 |

## 7. 已知约束：CLI 派的会话，同一个服务实例看不到

**这是本清单里最要紧的一条。**

headless 服务对菜单快照做了内存缓存，缓存只在**服务自己**处理变更时失效
（`internal/web/server.go` 的 `notifyMenuChanged`）。而 `launch` 是另一个进程，
服务完全不会感知，所以：

- 刚用 `launch` 派出去的会话，在**同一个**服务实例的 `/api/sessions` 与
  `/events/menu` 里都**看不到**，直到服务重启
- 上游没有提供纯刷新端点；能触发缓存失效的都是带副作用的写接口
- 数据本身确实落进了存储：**另起**一个服务实例就看得见

上游自己的注释承认了这点，原文（`server.go`）：

> In headless (--no-tui) mode there is no TUI loop to call
> publishWebMenuSnapshot(), so without this the menu snapshot would remain
> frozen at its first-load state and new sessions created via the API would
> never appear until server restart.

注意它说的是 *via the API*——经由服务自己的接口。经由 CLI 的，连这条路都没走上。

### Work Deck 对此的处置

1. 自己派出去的会话，**以绑定存储为准**，不等服务确认。`launch` 的返回值里
   已经有会话号、profile、工具和模型，够用。
2. 界面上对服务尚未感知的会话据实标注，不假装它看得见。
3. `TestIntegrationLaunchLandsInStorageEvenThoughServerCachesIt` 把这条约束
   钉成了断言：上游哪天修好，该测试会失败，那正是回来更新本节与视图逻辑的时候。

## 8. 上游变更时怎么核对

1. 跑 `go test ./internal/agentdeck/`，清单与代码不一致会直接失败。
2. 对照第 1、2 节，确认子命令与端点还在、参数名没改。
3. 对照第 3 节，确认字段名没改、语义没变（尤其 `session_id` 与 `status`）。
4. 第 4 节的环境变量若改名，adapter 的 profile 隔离会失效，**这是最危险的一条**。
