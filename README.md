# 安排 Anpai

> AI 干活，你拍板。

安排 Anpai 是跑在终端里的多 agent 工作台，基于 [upstream agent-deck](https://github.com/asheshgoplani/agent-deck)：
用 tmux 拼出一个四区布局（左侧按项目列会话的侧栏、中间是 agent 自己的原生 CLI、右侧决策与额度面板、下方通知），
由后台服务 `deckd` 记账。每个项目有一个主 agent，它按你设好的档位（最强、日常、便宜）把活派给子 agent；
超出预算要先建一个决定、等你批准；配置、推送、分歧这类事主 agent 也用 `deck agent escalate` 上报。
这些都排进右侧的「需要你决策」面板等你拍板。agent 与模型在 F9 设置里管。

命令是 `deck`（Go 写的，在 [`work/`](work/)）。仓库里还留着两个早先的 Python 启动器 `ai` 与 `host`，
见本文最后的「旧启动器」一节。

## 平台状态

| 平台 | 状态 |
|---|---|
| WSL 2 + Windows Terminal | 当前支持 |
| 其他 Linux 终端 | 没有专门验证 |
| Windows 原生、macOS | 不支持：`deckd` 用 Linux 的 `SO_PEERCRED` 与 `/proc` 认调用者 |

## 依赖

| 要什么 | 说明 |
|---|---|
| Go 1.27.1 或更新 | 只用来编译，版本以 [`work/go.mod`](work/go.mod) 为准 |
| Linux 内核 6.5 或更新 | `deckd` 用 `SO_PEERPIDFD` 绑定调用者的进程；内核不支持时，所有调用者都按「未知」处理，只能读、不能写。WSL 2 的内核通常已经满足 |
| tmux | 开发与实测用 3.4；代码里没有版本检查 |
| upstream agent-deck | 命令名 `agent-deck`，要在 `PATH` 里（或用环境变量 `WORKDECK_AGENTDECK_BIN` 指路）。对接并逐条核对过的是 v1.15.0（提交 `01c011b`），按上游项目自己的说明安装 |
| 至少一个 agent CLI | `claude`、`codex`、`opencode`、`grok`、`agy`（Antigravity）、`gemini`；在 F9 设置里按本机装了的加入 |

用 Claude Code 时有一条硬条件：upstream agent-deck 的 `config.toml`（`$XDG_CONFIG_HOME/agent-deck/config.toml`，
没设 `XDG_CONFIG_HOME` 就是 `~/.config/agent-deck/config.toml`；没有时回退 `~/.agent-deck/config.toml`）里
`[claude]` 要显式写 `dangerous_mode = false`。上游 v1.15 不写时默认给 Claude Code 加 `--dangerously-skip-permissions`，
deck 起 Claude 会话前会检查，不满足就拒绝启动，并在提示里说明怎么改。deck 不替你改这个文件。

额度面板和额度门读本机的 Token 看板（默认 `http://127.0.0.1:8899`，`deck daemon -dashboard` 可改）。
没有看板时，取不到的额度一律显示「未知」，不编数字。

## 安装

```bash
git clone https://github.com/Tingschen287/anpai.git
cd anpai/work
go build -o deck ./cmd/deck
install -D -m 0755 deck ~/.local/bin/deck
```

确认 `~/.local/bin` 在 `PATH` 里。`deck up` 会把 `deck` 自己的路径写进布局，所以请装到固定位置再用。
仓库原名 `terminal-deck`，改名后旧地址仍会跳转；Go 模块路径还是 `github.com/Tingschen287/terminal-deck/work`，没有改。

## 启动与常用按键

```bash
deck up
```

一条命令打开四区布局：没有布局就建，已有布局就直接接上；每次都会查 `deckd`，没在跑就在布局的后台窗口里把它起来。
在 tmux 里运行也可以（嵌套时它会清掉 `TMUX` 变量）。

第一次使用：

1. 按 F9 打开设置，在「项目」页按 `n`：选一个目录，确认项目 id（默认取目录名），回车建好。
   agent、三档模型和主 agent 先按全局清单给默认值，之后在「项目」页里改，改动自动保存。
2. 全局可用的 agent 在「全局 agent」页管：首次运行时已经有一份默认值，按 `a` 可以从本机装了的 CLI 里再加入。
3. 回到左侧栏，选中项目（标题行也能选），按 `m` 起主 agent。它会出现在项目下，名字前有 ★。
4. 点左栏的会话，它就换进中间；在中间直接和 agent 对话。

布局上的按键由布局这台 tmux 截获，不会进到中间的 CLI（布局没有前缀键，也没有状态栏）：

| 键 | 作用 |
|---|---|
| F2 | 接管中间正在显示的子 agent（切走、隐藏或 Ctrl-Q 即自动交还） |
| Ctrl-Q | 把中间正在看的会话放回后台，会话照常运行 |
| F5 / F6 / F7 | 显示或隐藏左栏 / 右栏 / 下栏 |
| F8 | 把排队等着转告主 agent 的汇报立刻送出 |
| F9 | 打开 agent 设置 |
| F10 | 键盘焦点回到中间窗格 |

子 agent 在中间默认只读；主 agent 的第一条 brief 确认送达后可写。CLI 启动时停在目录信任等确认画面上时，
deck 不替你选、也不发任务，侧栏和通知会说明，你在中间自己选，选完它再继续。

左侧栏（鼠标点击和滚轮也可以）：

| 键 | 作用 |
|---|---|
| `↑` `↓`（或 `k` `j`） | 选择项目标题或会话 |
| `Enter`（或 `o`） | 把选中的会话换进中间 |
| `h` | 把选中且已打开的会话放回后台 |
| `m` | 为选中的项目起主 agent |
| `Esc` | 键盘焦点回到中间 |

会话前的圆点：绿色在跑，黄色等你输入，灰色空闲，红色出错，空心为未知。
右栏的「需要你决策」面板里用 `↑` `↓` 选，按数字键作答。

关掉终端窗口只是断开，布局和 `deckd` 继续在后台跑，再运行 `deck up` 接回。
要彻底停掉，关掉布局这台 tmux：`tmux -S "${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/deck" kill-server`。
`deckd` 随之停止，agent 会话照常在 agent-deck 那台 tmux 上跑，下次 `deck up` 再把 `deckd` 起来。

### 其他命令

| 命令 | 作用 |
|---|---|
| `deck settings -check` | 只读自查设置文件，不需要 `deckd`，不写任何东西。升级前先跑 |
| `deck project add --id <id> --name <名字> --path <目录> [--main <agent:档位>] [--agents a,b] [--model 档位=模型,…]` | 命令行新建项目（`deckd` 要在运行） |
| `deck agent start-main --project <项目>` | 为项目起主 agent，和侧栏按 `m` 同一套准入 |
| `deck agent spawn --tier <档位> --title <标题> --task-file <文件>` | 主 agent 用来派子 agent；档位是 `economy`、`balanced`、`frontier`，也可以用 `--model` 点名模型。完整的子命令用 `deck agent <子命令> -h` 看 |
| `deck panel <名字> [-project id]` | 在当前窗格里跑一块面板：`decisions`、`quota`、`progress`、`notifications`、`activity` |
| `deck reports deliver-now [--project id]` | 与 F8 相同 |
| `deck whoami` | `deckd` 把当前进程认成谁 |
| `deck daemon` | 只起 `deckd`。平时不用，`deck up` 会替你起 |

`deck agent` 的子命令按调用者分权限：布局窗格里的是你（owner），注册表里的会话是主 agent 或子 agent，其他进程是未知，只能读。
`deck agent` 退出码 3 表示在等 owner 批准，输出里有决定与下一步。

## 配置与数据

路径跟随 XDG 变量，没设就用括号里的默认值。

| 位置 | 内容 |
|---|---|
| `$XDG_CONFIG_HOME/deck`（`~/.config/deck`） | `agents.yaml`（全局可用的 agent 与模型档位，首次运行生成默认值）、`projects.yaml`（每个项目用哪些 agent、三档各排哪些模型、预算）、`panels.yaml`（面板开关） |
| `$XDG_STATE_HOME/deck`（`~/.local/state/deck`） | `deckd` 的状态与 `deckd.sock`、`deckd.lock`；`deck up` 生成的 `layout.conf`、`keys.conf`；当前 logo 画法 `graphics`；中间窗格重连日志 `center.log`。目录只给本用户 |
| `$XDG_STATE_HOME/agent-deck-writers/<profile>.lock` | 同一个 agent-deck profile 只许一个写入者，`deck` 默认用 profile `deck` |
| `$TMUX_TMPDIR/tmux-<UID>/deck`、`…/default` | 布局这台 tmux 与 agent-deck 那台 tmux 的 socket（`TMUX_TMPDIR` 没设时是 `/tmp`） |

设置改动在 F9 里自动保存，对之后起的会话生效，已在跑的会话不变。手改 `agents.yaml` 与 `projects.yaml` 时，
两份文件的 `generation` 要保持一致，否则 `deckd` 拒绝启动并提示怎么恢复。`panels.yaml` 的 `on` 控制面板挂不挂，
改后要重建布局（关掉布局这台 tmux 再 `deck up`）才生效；默认挂着决策、额度和下栏通知。

环境变量：

| 变量 | 作用 |
|---|---|
| `DECK_GRAPHICS` | logo 画法 `sixel`、`blocks`、`none`（也可以 `deck up -graphics`）；不设时 `deck up` 问终端：支持 Sixel 且知道一格多少像素就用 Sixel，否则用品牌色字母徽章 |
| `WORKDECK_AGENTDECK_BIN` | agent-deck 可执行文件，默认在 `PATH` 里找 `agent-deck` |
| `DECK_AGENTDECK_ADDR`、`WORKDECK_AGENTDECK_TOKEN` | 改连现成的 agent-deck headless 服务读会话状态（等同 `deck up -upstream`）。不设时 `deckd` 调 `agent-deck list --json` 读，没有端口也没有 token |
| `WORKDECK_PROJECTCTL` | 进度面板用的 Graph 控制器脚本路径（可选） |

## 与 upstream agent-deck 的关系，以及重名说明

安排 Anpai **基于 upstream agent-deck**（[asheshgoplani/agent-deck](https://github.com/asheshgoplani/agent-deck)，MIT 许可，Go 写的）：
会话的创建、投递、停止、列表经它的命令行完成，本仓不含也不修改它的源码。
对接的命令、参数与依赖逐条记在 [`work/internal/agentdeck/MANIFEST.md`](work/internal/agentdeck/MANIFEST.md)，上游升级时拿它核对。
`deck` 用 agent-deck 的 profile `deck`，agent 会话都建在 agent-deck 那台 tmux 上。

本产品原名 Agent Deck，与 upstream agent-deck 同名且简介相近，2026-10-15 改名为「安排 Anpai」（英文 Anpai）。
改的是界面上看得到的名字和仓库名；命令名 `deck`、`deckd`、配置与数据路径、环境变量、按键都没变。
所以：

- 文档里说 **upstream agent-deck** 就是上游那个程序。
- 代码里的包名 `internal/agentdeck` 是对接上游的适配层。
- 下面「旧启动器」一节里的 `ai` 程序自己仍然叫 Agent Deck（`ai --version` 输出 `Agent Deck 0.5.2`），它是本仓早先的 Python 启动器，和上游没有关系，也不在这次改名范围内。

## 开发校验

```bash
cd work
go vet ./...
go test -count=1 ./...
```

离线环境可以加 `GOTOOLCHAIN=local GOPROXY=off GOFLAGS=-mod=readonly`。回到仓库根，旧启动器的校验：

```bash
python3 -m unittest discover -s tests -v
bash -n scripts/install.sh scripts/uninstall.sh
```

更详细的功能说明、设计边界和各项约束见 [`work/README.md`](work/README.md)。

## 旧启动器：`ai` 与 `host`

仓库里还有两个 Python 写的启动器，现在仍可用，不在安排 Anpai 的范围内，也不依赖 Go：

| 命令 | 干什么 | 真正干活的程序 |
|---|---|---|
| `ai` | 选 agent、选目录、开会话（自称 Agent Deck） | `claude` / `codex` / `grok` / `kimi` / `opencode` 等原生 CLI |
| `host` | 选服务器、分组、连接（Host Deck） | 本机 OpenSSH |

需要 Python 3.11+ 和 Bash。安装：

```bash
./scripts/install.sh
```

脚本把共享 TUI 与两个启动器装到 `~/.local/lib/deck/`，把 `~/.local/bin/ai` 和 `~/.local/bin/host` 链过去；
配置不存在时才创建 `~/.config/ai-launcher/agents.toml` 与 `~/.config/host-deck/hosts.toml`，升级时保留已有配置。
确认 `~/.local/bin` 在 `PATH` 里后：

```bash
ai --list
host --list
```

常用命令：

```text
ai                          选择 Agent，再选择目录
ai codex                    固定 Agent，只选择目录
ai cco ~/dev/project        直接在指定目录启动
ai --resume codex ~/dev/project
ai kimi ~/dev/project -- --help    "--" 后面的参数原样传给 Agent CLI
ai --shell                  选择目录后进入纯 Shell
ai --version

host                        选择主机后连接
host example-dev            直接连接该 Host 别名
host --attach example-dev   连接后进入或创建 tmux
host example-dev -- -v      额外参数原样传给 ssh
host --import-tabby         从 Tabby 导入（不改 Tabby）
host --version
```

`host` 的连接事实以 `~/.ssh/config` 为准，密码进 Windows 凭据库，不进配置文件、不进 Git。
配置样例和各字段的含义写在样例文件的注释和字段里：
[`config/agents.example.toml`](config/agents.example.toml)、[`config/hosts.example.toml`](config/hosts.example.toml)。
可用 `AI_LAUNCHER_CONFIG`、`AI_LAUNCHER_HISTORY`、`HOST_DECK_CONFIG` 覆盖配置和历史路径。
Windows Terminal 的启动项样例在 [`integrations/windows-terminal/profiles.example.jsonc`](integrations/windows-terminal/profiles.example.jsonc)，
合并进 `settings.json` 后替换 `<DISTRO>`、`<WSL_USER>`。

更新与卸载：

```bash
git pull --ff-only
./scripts/install.sh
./scripts/uninstall.sh            # 保留配置和历史
./scripts/uninstall.sh --purge    # 连配置和历史一起删
```

版本记录见 [`CHANGELOG.md`](CHANGELOG.md)（只记旧启动器）。

## License

尚未指定开源许可证。公开分发前由仓库所有者选择。
