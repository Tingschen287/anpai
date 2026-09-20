# upstream agent-deck 依赖清单

Work Deck 对 upstream agent-deck 的全部依赖列在这里。上游升级时，拿这份清单逐条核对。

- 这里说的 **upstream agent-deck** 是开源项目 `github.com/asheshgoplani/agent-deck`，
  **不是**本仓自己那个同名的 Agent Deck 启动器。约定见项目的 `2.code/link.md`。
- 核对过的版本：**Agent Deck v1.15.0**，快照提交 `01c011b`
- 机器可读的同一份清单在 `manifest.go`，`manifest_test.go` 盯着两者与真实代码一致

## 1. 命令行子命令

| 子命令 | 用途 | 用到的参数 |
|---|---|---|
| `launch` | 派工：建会话、起会话、可选发首条消息 | `-c`（工具）、`--model`（模型 ID）、`--json`、`-t`（标题）、`-g`（分组）、`-m`（首条消息）、`--no-wait` |
| `web` | 起 headless HTTP 服务 | `--no-tui`、`--listen`、`--token` |

清单之外的子命令，adapter 会直接拒绝执行（`ErrSubcommandNotAllowed`）。

## 2. HTTP 端点

服务地址固定为回环地址，带 bearer token。实测无 token 返回 **HTTP 401**。

| 端点 | 用途 | 说明 |
|---|---|---|
| `GET /api/sessions` | 冷启动取一次全量会话 | 返回 `sessions[]`、`groups[]`、`profile` |
| `GET /events/menu` | SSE 实时菜单 | 每次推**全量**快照，不是增量，收到整份替换 |

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
`command`、`createdAt`、`lastAccessedAt`

### 菜单事件顶层

`profile`、`generatedAt`、`totalGroups`、`totalSessions`、`items`

## 4. 环境变量

| 变量 | 用途 |
|---|---|
| `AGENTDECK_PROFILE` | 指定隔离 profile。**adapter 强制要求显式指定**，留空直接报错 |

留空会落到上游默认 profile。上游自己记录过一次事故（2025-12-11）：测试跑在真实
profile 上，一次覆盖掉 36 个生产会话。所以这里不接受默认值。

## 5. tmux 约定

- 会话名形如 `agentdeck_<title>_<hash>`，由上游生成
- Work Deck **只读**这个名字，从 `tmuxSession` 字段取，不自己按标题推算
- 本轮不 attach、不发按键（那是 n037 的事）

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
