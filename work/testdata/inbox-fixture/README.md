# inbox 夹具

upstream agent-deck `inbox export` 读的三处记录，按上游格式手写，供 `internal/queue` 的集成测试用。
测试把本目录拷进临时的 `$XDG_DATA_HOME/agent-deck/`，不碰真实数据目录，也不启动 notify-daemon；
心跳文件 `runtime/notify-daemon.heartbeat` 由测试按需写（新鲜 = daemon 在跑，陈旧或没有 = 不在）。

- `inboxes/parent-1.jsonl`：父会话 inbox，一行一条。child-a 等待、child-b 出错（auth-401）、child-d 翻到 idle（不需要人拍板）。
- `inboxes/_unowned.jsonl`：与 parent-1 里 child-a 那条完全相同，用来看上游与队列的去重。
- `runtime/completion-ledger/child-c.json`：完成账本，export 会合成一条 kind=finished 的记录。

记录的 profile 都写 `_n038fixture`：Work Deck 的队列只收本客户端 profile 的记录，测试用同名 profile 的客户端去读。
上游 export 会为记录里出现的每个 profile 打开注册表、没有就建 `profiles/_n038fixture/state.db`——这是有文档的副作用，
落在临时 XDG_DATA_HOME 里，不碰真实数据目录。
