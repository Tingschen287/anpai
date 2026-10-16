# Agent Deck · Host Deck

同一个仓库里的两个终端入口，共用一套 TUI：

| 产品 | 命令 | 干什么 | 真正干活的程序 |
|---|---|---|---|
| **Agent Deck** | `ai` | 选 Agent、选目录、开会话 | `claude` / `codex` / `grok` / `kimi` / `opencode` 等原生 CLI |
| **Host Deck** | `host` | 选服务器、分组、连接 | 本机 OpenSSH |

启动器只负责发现、选择、分组和交接。不替代 Agent CLI，也不自己实现 SSH。

GitHub 仓库名是 `terminal-deck`。当前运行时是 WSL + Windows Terminal。

## 平台状态

| 平台 | 状态 |
|---|---|
| WSL 2 + Windows Terminal | 当前支持 |
| Linux terminal | 核心流程可用；无 Windows Terminal 页签交接 |
| Windows native | 规划中 |
| macOS | 规划中 |

## 安装

```bash
git clone https://github.com/Tingschen287/terminal-deck.git
cd terminal-deck
./scripts/install.sh
```

需要 Python 3.11+ 和 Bash。安装脚本会：

- 把共享 TUI、Agent Deck、Host Deck 装到 `~/.local/lib/deck/`
- 把 `~/.local/bin/ai` 和 `~/.local/bin/host` 链过去
- 配置不存在时才创建 `agents.toml` / `hosts.toml`
- 升级时保留已有配置

确认 `~/.local/bin` 已在 `PATH`：

```bash
ai --list
host --list
```

## Agent Deck

选 Agent 和工作目录，再调用本机已安装的原生 CLI。菜单、目录历史、代理、环境变量、PATH、Windows Terminal 页签由启动器管。

```text
ai                         选择 Agent，再选择目录
ai codex                   固定 Agent，只选择目录
ai cco ~/dev/project       直接在指定目录启动
ai --resume codex ~/dev/project
ai kimi ~/dev/project -- --help
ai --shell                 选择目录后进入纯 Shell
ai --list                  查看 Agent 注册表
ai --version               查看版本
```

`--` 后面的参数原样传给 Agent CLI。目录页标题是 `New | Resume`，默认 `New`。Resume 用各 Agent 自己的语法：

| Agent | 实际命令 |
|---|---|
| Claude 官方 / CC-Switch | `claude --resume` |
| Codex | `codex resume` |
| Grok | `grok "/resume"` |
| Kimi | `kimi --session` |
| OpenCode | `opencode --continue` |

配置：`~/.config/ai-launcher/agents.toml`，样例见 [`config/agents.example.toml`](config/agents.example.toml)。可用 `AI_LAUNCHER_CONFIG`、`AI_LAUNCHER_HISTORY` 覆盖路径。

| 字段 | 含义 |
|---|---|
| `key` | CLI 短名与数字菜单标识 |
| `name` | 菜单显示名称 |
| `cmd` | 最终执行的本机原生命令 |
| `resume_args` | 打开原生 Resume 页时追加的参数 |
| `color` | `#RRGGBB` |
| `note` | 菜单备注 |
| `proxy` | `true` 走 `proxy-on --quiet`；`false` 清代理、直连 |
| `path_prepend` | 启动前加到 PATH 前面 |
| `env` / `unset` | 启动前设置或清除的环境变量 |
| `default_dir` | Agent 专属默认目录 |
| `wt_profile` | Windows Terminal 隐藏 Profile 名 |

Agent 退出后保留 Shell。

## Host Deck

在 Windows Terminal 里选 SSH 主机并连接，少切一次 Tabby。连接事实以 `~/.ssh/config` 为准。Host Deck 只存显示名、分组、颜色、收藏等编排信息。密码进 Windows 凭据库，不进配置文件、不进 Git。

```text
host                       选择主机后连接
host example-dev           直接连接该 Host 别名
host --attach example-dev  连接后进入或创建 tmux
host example-dev -- -v     额外参数原样传给 ssh
host --list                查看发现的主机
host --import-tabby        从 Tabby 导入（不改 Tabby）
host --version             查看版本
```

列表约 88 列宽。分组标题可点，折叠或展开。每条右侧 `▶` 连接、`✎` 编辑（键盘 `e`）。按 `n` 添加主机，只在 `~/.ssh/config` 末尾追加，不改已有 Host。

标题栏 `Connect | Attach`，默认 Connect。Attach 会进远程 tmux（有 `tmux_session` 则 `tmux new-session -A -s <name>`）。

主机来自 `~/.ssh/config` 的 `Host` 别名（跟随 `Include`，跳过通配）。最终参数用 `ssh -G <alias>`，不自己解析 HostName/User/Port/IdentityFile。

在 WSL 里默认走 Windows 网络（TCP 代理 + OpenSSH），这样能连上 Tabby 能连的那些内网机器，并用凭据库自动填密码。只要 WSL 网络的主机，在 `hosts.toml` 里设 `via = "wsl"`。

从 Tabby 导入：读 Tabby 的 `config.yaml`，追加 Host 和显示名/分组；能对上的密码拷到 Host Deck 自己的凭据项。Tabby 不改。再导一次会跳过已导入的，但会刷新密码。

配置：`~/.config/host-deck/hosts.toml`。安装时若没有，写入空 bootstrap。字段见 [`config/hosts.example.toml`](config/hosts.example.toml)。不要把密码写进去。

连接失败后保留 Shell。Host Deck 选择器页签会留下，方便再连下一台。

## Windows Terminal

[`integrations/windows-terminal/profiles.example.jsonc`](integrations/windows-terminal/profiles.example.jsonc) 里有可见的 `Agent Deck` / `Host Deck`，以及各 Agent 和通用 `SSH (WSL)` 的隐藏 Profile。

1. 合并进 `settings.json` 的 `profiles.list`。
2. 替换 `<DISTRO>`、`<WSL_USER>`。
3. GUID 冲突就重新生成。
4. `icon` 按机器自己加，不要提交进仓库。

从对应 Profile 进入时，会在同一窗口开新页签。没有匹配 Profile 或 `wt.exe` 时，留在当前终端启动。

## 更新与卸载

```bash
git pull --ff-only
./scripts/install.sh
```

```bash
./scripts/uninstall.sh            # 保留配置和历史
./scripts/uninstall.sh --purge    # 连配置和历史一起删
```

卸载不会改 Tabby，也不会清 Windows 凭据库里的密码。

## 开发校验

```bash
python3 -m unittest discover -s tests -v
bash -n scripts/install.sh scripts/uninstall.sh
```

## Roadmap

- 抽离终端输入、进程启动和页签集成的 platform adapters
- Windows 原生 Terminal / PowerShell
- macOS Terminal / iTerm2
- 可选的配置初始化向导

## License

尚未指定开源许可证。公开分发前由仓库所有者选择。
