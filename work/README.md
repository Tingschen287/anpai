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

看一个 Graph 项目的全貌，派工，进入会话，派评审。

| 按键 | 做什么 |
|---|---|
| `↑↓` `Enter` `Tab` | 选节点、看详情、展开已验收 |
| `d` | 给 ready 节点派工；给 submitted 节点派评审 |
| `a` | 把终端交给会话，走上游的 `session attach` |
| `T` | 绕开上游，直连会话所在的 tmux |
| `p` | 只读预览会话画面，不接管 |
| `r` `q` | 刷新、退出 |

**对 Graph 控制器这一侧仍然是只读的。**

- 不往事件账本写一个字节——开工、提交、验收都由人在命令行上做
- 拿不到的数据显示成「未知」，不填一个看起来合理的数字

这不是口头约定，`internal/projectctl` 的测试逐条盯着：控制器命令白名单只有四条
只读命令，写命令在包里根本没有对应的方法，`exec` 只准出现在两个 adapter 包内，
包外任何地方碰账本文件都会让测试失败。

**对 upstream agent-deck 那一侧会写。** 派工要建会话、派评审要投递 brief，
这些本来就是写操作。哪条命令在上游留下什么，`internal/agentdeck/MANIFEST.md`
有一张逐条的表，每条带上游源码依据，测试拿依据去上游快照核对。

> 这一节在 n035 时写的是「只读。它读，不写」，那时确实如此。
> n036 加了派工、n037 加了进入会话与派评审之后它就不对了，却一直留到
> 第八轮评审才被指出来。**能力变了，README 要跟着变。**

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

状态投影是控制器写命令的副产物，只读的 Work Deck 不会去刷新它，所以它随时可能
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

## 测试

```bash
go test ./...
```

夹具在 `testdata/workdeck-fixture`，是一个七个节点分别停在七种状态的假项目，
账本由控制器亲自写出，和真项目同构。要重建它：

```bash
bash testdata/rebuild-fixture.sh
```
