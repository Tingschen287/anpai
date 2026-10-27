// Package sessionenv 解决「deckd 找 agent CLI 用的 PATH，和 agent-deck 起的会话用的 PATH 不是同一个」（n060 段 4）。
//
// 实测事实（agent-deck v1.15.0 darwin，隔离 HOME 与假 CLI，证据见 4.eval/results/n060_ui_i18n/stage-4/path-probe/）：
//   - agent-deck 起的会话，PATH 就是**调用 agent-deck launch 的那个进程**（这里是 deckd）的 PATH，原样传进会话；
//   - 它不读 agent-deck tmux server 的全局环境（server 先于会话存在、PATH 不同，会话仍用启动者的），
//     也不 source 用户的 .zshrc / .bashrc / .zprofile。
//
// 所以「会话 PATH」不是一个能从别处读出来的现成值，而是 deckd 自己决定、再交给 agent-deck 的值。本包就是做这个决定：
//  1. 解析 owner 的登录交互 shell 的 PATH（owner 平时在终端里敲 codex、kimi 时用的那个）；
//  2. 与 deckd 自己进程的 PATH 合并：登录 shell 里 deckd 没有的目录（owner 习惯的新版本所在，例如 ~/.kimi-code/bin）加在最前面，
//     deckd 自己的目录保持原来的顺序跟在后面；
//  3. deckd 用这个 PATH 找 CLI（设置里的已装与版本、Codex trust 门记下的可执行文件与版本、列模型），
//     并在 `agent-deck launch` 时把同一个 PATH 交给它（agentdeck.WithSessionPath）。
//     PATH 相同本身不保证上游找到的是同一个文件（它按工具名再找一次）；Codex 授权通过后 deckd 另把核准文件所在的目录放到最前面（FrontDir），
//     这只绑定路径，同一路径上的文件被替换是既有的信任边界（n060 段 4 返工，R1）。
//
// 取不到登录 shell 的 PATH（没有可用的 shell、超时、输出不对）时回退为 deckd 自己的 PATH，并带一条可译的原因，由界面提示。
// 不改 owner 的任何 shell 配置；解析有超时，超时杀掉整个进程组；输出带随机边界，rc 文件打印的杂音不会被当成 PATH。
package sessionenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Source 说明会话 PATH 是怎么来的。
type Source string

const (
	// SourceLoginShell：用了登录交互 shell 的 PATH（并与 deckd 自己的合并）。
	SourceLoginShell Source = "login-shell"
	// SourceProcess：没有取到登录 shell 的 PATH（或没有开解析），只有 deckd 自己进程的 PATH。
	SourceProcess Source = "process"
)

// ProbeEnv 是解析期间设进子进程环境的变量。owner 的 rc 文件里如果会起 deck（或别的重东西），可以用它短路；
// deckd 看到它已经设了就不再解析，免得 rc 里又起 deckd 造成递归。
const ProbeEnv = "ANPAI_PATH_PROBE"

const (
	maxOutput  = 64 << 10 // 登录 shell 输出的上限
	maxEntries = 256      // 合并后最多多少个目录
	maxPathLen = 32 << 10 // 合并后的 PATH 最长
	maxEntry   = 4096     // 单个目录最长
	probeWait  = 5 * time.Second
)

// Snapshot 是一次解析的结果，不可变；用的人整份拿走，不再回头读 Resolver，这样一次派工里 trust 门与启动用的是同一个 PATH。
type Snapshot struct {
	Path     string // 冒号分隔的绝对目录：登录 shell 里 deckd 没有的在最前，deckd 自己的按原顺序跟在后面
	Source   Source
	Shell    string // 用了哪个 shell（只有 SourceLoginShell）
	Fallback error  // 没取到登录 shell 的 PATH 的原因（*i18n.Error）；Source 为 SourceLoginShell 时为 nil，没开解析时也为 nil
	Dropped  int    // 丢掉的不合格目录条数（相对路径、含控制字符、过长……）
	At       time.Time
}

// Resolver 解析并持有会话 PATH 的快照。零值不能用，用 NewLoginResolver 或 NewStatic。
//
// 什么时候会运行登录 shell（也就是运行 owner 的 rc 文件）——只有两个时机（n060 段 4 返工，owner 2026-10-27 定）：
//  1. deckd 启动时后台读一次（Start，或第一次有人要用时），失败也只读这一次；
//  2. owner 在 F9 里按「刷新会话 PATH」键时（Refresh）。
//
// 除此之外一律不运行：打开设置窗口、进加入页和 agent 页、派工、列模型，用的都是当前那份快照，快照不会因为「过期」被重读
// （以前的「成功 5 分钟、失败 30 秒、Fresh 10 秒」过期重读都去掉了）。
//
// 不互相卡住：
//   - 读在自己的后台 goroutine 里跑，Get 平时只是读一份指针，不等任何正在跑的读；刷新期间派工、列模型、显示照常用旧快照；
//   - 一次只允许一个读在跑：读的过程中再按键（同一个窗口连按、几个窗口同时按）合并为同一次，不叠加；
//   - 第一次读还没完成就有人要用（deckd 刚启动）：等这一次，上限是探针超时 Wait（5 秒）加收拾进程组的 WaitDelay（0.5 秒）。
//
// 读失败：启动那次失败，快照退回 deckd 自己的 PATH 并带原因（Snapshot.Fallback），界面提示 owner 可以按刷新键重试；
// 按键刷新失败，已有的好快照保留（不让一次失败把 owner 习惯的目录弄丢），失败原因记在 State.RefreshErr，由界面提示；
// 若当时快照本来就是启动失败的回退，则换成最新这次的回退（原因更新）。
type Resolver struct {
	// 下面几个字段给测试换；生产里用 NewLoginResolver 填好的。
	Getenv  func(string) string
	Environ func() []string
	Run     RunFunc
	Now     func() time.Time
	Wait    time.Duration // 一次解析最多等多久
	// Base 是后台读的父 ctx：deckd 关闭时取消，探针的进程组随之被杀。nil 时用 context.Background()。
	Base context.Context

	static bool       // NewStatic：不解析，直接用进程 PATH
	mu     sync.Mutex // 只保护下面几个字段，不在运行 shell 的时候持有
	cur    *Snapshot  // 当前快照；第一次读完成前为 nil
	flight *flight    // 正在跑的那一次读；没有则为 nil
	rerr   error      // 最近一次按键刷新失败的原因；成功后清空
}

// flight 是一次正在后台跑的读。done 在读完（快照已更新）后关闭。
type flight struct{ done chan struct{} }

// State 是快照加上读的状态，给界面显示用。
type State struct {
	Snapshot   Snapshot
	Refreshing bool  // 有一次读正在跑（启动时的、或按键触发的）
	RefreshErr error // 最近一次按键刷新失败的原因（*i18n.Error）；没有失败过或之后成功了为 nil
}

// RunFunc 运行解析用的 shell 并返回它的标准输出：stdin 接 /dev/null、输出有上限、结束时清理原进程组、没有控制终端。
// 本包不自己起进程（exec 只许出现在两个 adapter 包里，TestEachAdapterOnlyTouchesItsOwnUpstream）；生产用 agentdeck.RunLoginShell。
type RunFunc func(ctx context.Context, shell string, args []string, env []string) ([]byte, error)

// NewLoginResolver 返回会解析登录 shell 的 PATH 的 Resolver（deckd 生产用）。run 为 nil 时不解析（与 NewStatic 一样只用进程自己的 PATH）。
func NewLoginResolver(run RunFunc) *Resolver {
	return &Resolver{Getenv: os.Getenv, Environ: os.Environ, Run: run, Now: time.Now, Wait: probeWait}
}

// NewStatic 返回不解析的 Resolver：PATH 一直是进程自己的（没有开解析、测试默认）。
func NewStatic() *Resolver {
	return &Resolver{Getenv: os.Getenv, Environ: os.Environ, Now: time.Now, static: true}
}

func (r *Resolver) disabled() bool { return r == nil || r.static || r.Run == nil }

// Start 在后台读第一次（已经读过或正在读就什么都不做）。deckd 启动时调用。立即返回。
func (r *Resolver) Start() {
	if r.disabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil && r.flight == nil {
		r.startLocked(false)
	}
}

// Get 返回当前的会话 PATH 快照。不会因为「过期」重读；第一次读还没完成时等这一次（上限见 Resolver 的说明）。
// 调用方（一次 HTTP 请求）取消不影响读：读在后台自己的 goroutine 里跑，有自己的超时。
func (r *Resolver) Get(ctx context.Context) Snapshot { return r.State(ctx).Snapshot }

// State 同 Get，另带读的状态。
func (r *Resolver) State(ctx context.Context) State {
	for {
		r.mu.Lock()
		if r.cur == nil && r.disabled() {
			s := r.resolve(ctx) // 不解析的：只是读一下进程自己的 PATH，不起进程
			s.At = r.now()
			r.cur = &s
		}
		if r.cur != nil {
			st := State{Snapshot: *r.cur, Refreshing: r.flight != nil, RefreshErr: r.rerr}
			r.mu.Unlock()
			return st
		}
		f := r.flight
		if f == nil {
			f = r.startLocked(false) // 没人启动过：第一个要用的人触发（deckd.Run 会先 Start）
		}
		r.mu.Unlock()
		<-f.done
	}
}

// Refresh 是「刷新会话 PATH」键：在后台重读一次登录 shell 的 PATH，立即返回。
// 已经有一次读在跑（启动那次、别的窗口按的、同一个窗口连按）就合并进去，不再起新的，返回 false；真的起了新的读返回 true。
// 没有开解析（NewStatic、Run 为 nil）时什么都不做，返回 false。
func (r *Resolver) Refresh() bool {
	if r.disabled() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flight != nil {
		return false
	}
	r.startLocked(true)
	return true
}

// Enabled 说明这个 Resolver 会不会真的运行登录 shell（界面据此决定要不要显示刷新键）。
func (r *Resolver) Enabled() bool { return r != nil && !r.disabled() }

// startLocked 在后台起一次读（调用方持有 mu，且当前没有读在跑）。读完更新快照、清掉 flight、关闭 done。
func (r *Resolver) startLocked(manual bool) *flight {
	f := &flight{done: make(chan struct{})}
	r.flight = f
	base := r.Base
	if base == nil {
		base = context.Background()
	}
	go func() {
		s := r.resolve(base) // 自己有超时（Wait）；不持有 mu
		s.At = r.now()
		r.mu.Lock()
		switch {
		case r.cur == nil:
			r.cur = &s
		case !manual: // 不会发生：已有快照时只有按键触发读
			r.cur = &s
		case s.Fallback == nil:
			r.cur, r.rerr = &s, nil
		default:
			if r.cur.Fallback != nil {
				r.cur = &s // 本来就是回退：换成最新一次的回退（原因更新）
			}
			r.rerr = s.Fallback
		}
		r.flight = nil
		r.mu.Unlock()
		close(f.done)
	}()
	return f
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) getenv(k string) string {
	if r.Getenv != nil {
		return r.Getenv(k)
	}
	return os.Getenv(k)
}

func (r *Resolver) resolve(ctx context.Context) Snapshot {
	own, ownDropped := cleanList(filepath.SplitList(r.getenv("PATH")))
	proc := Snapshot{Path: strings.Join(own, ":"), Source: SourceProcess, Dropped: ownDropped}
	if r.static || r.Run == nil {
		return proc
	}
	if r.getenv(ProbeEnv) != "" {
		proc.Fallback = i18n.E("err.sessionenv.recursion", ProbeEnv)
		return proc
	}
	shell, err := pickShell(r.getenv("SHELL"))
	if err != nil {
		proc.Fallback = err
		return proc
	}
	wait := r.Wait
	if wait <= 0 {
		wait = probeWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	mark, err := randomMark()
	if err != nil {
		proc.Fallback = i18n.E("err.sessionenv.failed", shell, err)
		return proc
	}
	// 随机边界：rc 文件里打印的任何东西（含伪造的 PATH=…）都不会被当成结果
	script := `printf '\n%s%s%s\n' '` + mark + `B' "$PATH" '` + mark + `E'`
	env := append(append([]string{}, r.environ()...), ProbeEnv+"=1")
	out, err := r.Run(ctx, shell, []string{"-l", "-i", "-c", script}, env)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			proc.Fallback = i18n.E("err.sessionenv.timeout", shell, wait.String())
		} else {
			proc.Fallback = i18n.E("err.sessionenv.failed", shell, err)
		}
		return proc
	}
	login, ok := extract(out, mark)
	if !ok {
		proc.Fallback = i18n.E("err.sessionenv.nooutput", shell)
		return proc
	}
	entries, dropped := cleanList(filepath.SplitList(login))
	if len(entries) == 0 {
		proc.Fallback = i18n.E("err.sessionenv.nooutput", shell)
		return proc
	}
	merged := merge(entries, own)
	if len(merged) > maxEntries || len(strings.Join(merged, ":")) > maxPathLen {
		proc.Fallback = i18n.E("err.sessionenv.toolong", shell)
		return proc
	}
	return Snapshot{Path: strings.Join(merged, ":"), Source: SourceLoginShell, Shell: shell, Dropped: dropped + ownDropped}
}

func (r *Resolver) environ() []string {
	if r.Environ != nil {
		return r.Environ()
	}
	return os.Environ()
}

// merge：登录 shell 里 deckd 自己没有的目录，按登录 shell 的顺序加在最前面；deckd 自己的目录保持它原来的顺序跟在后面；去重。
// 为什么不是「登录 shell 的整串在前」：deckd 是 owner 从某个环境起来的（例如 PATH 里特意把某个目录排在 /usr/bin 前面），这个顺序要保留；
// 登录 shell 在 macOS 上会被 path_helper 把系统目录排到最前，整串放前面就把 owner 排在前面的目录压到系统目录后面了
// （旧 e2e 的假 CLI 用 `env python3`，被 /usr/bin/python3 顶掉，启动后核对认不出进程，会话被停；e2e60、e2e55、e2e57 实测）。
// 要让 owner 习惯的新版本赢：问题出在 deckd 的 PATH 里没有的目录（~/.kimi-code/bin、~/.npm-global/bin），它们都排在最前。
func merge(login, own []string) []string {
	inOwn := map[string]bool{}
	for _, d := range own {
		inOwn[d] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range login {
		if !inOwn[d] && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, d := range own {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// cleanList 只留合格的目录：绝对路径、没有控制字符、不过长。相对路径（含空串与 .）会让 PATH 查找跟着当前目录走，
// 一个被别的程序写进 PATH 的相对目录就能顶替 owner 想用的 CLI，所以一律丢掉。返回目录与丢掉的条数。
func cleanList(in []string) ([]string, int) {
	var out []string
	dropped := 0
	for _, d := range in {
		if !filepath.IsAbs(d) || len(d) > maxEntry || hasControl(d) || i18n.HasMark(d) {
			dropped++
			continue
		}
		out = append(out, filepath.Clean(d))
	}
	return out, dropped
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// extract 从输出里取最后一对边界之间的内容。
func extract(out []byte, mark string) (string, bool) {
	b, e := []byte(mark+"B"), []byte(mark+"E")
	i := bytes.LastIndex(out, b)
	if i < 0 {
		return "", false
	}
	rest := out[i+len(b):]
	j := bytes.Index(rest, e)
	if j < 0 {
		return "", false
	}
	return string(rest[:j]), true
}

func randomMark() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ANPAI" + hex.EncodeToString(b[:]), nil
}

// fallbackShells 是 $SHELL 不可用时按顺序试的系统 shell（var 只为让测试换掉）。
var fallbackShells = []string{"/bin/zsh", "/usr/bin/zsh", "/bin/bash", "/usr/bin/bash", "/bin/sh"}

// pickShell 选解析用的 shell：$SHELL 要是绝对路径、存在、可执行、名字是认得的类 POSIX shell 或 fish；
// 否则按系统常见位置找 zsh、bash、sh。都没有就报错（调用方回退）。
func pickShell(env string) (string, error) {
	known := map[string]bool{"zsh": true, "bash": true, "sh": true, "dash": true, "ksh": true, "fish": true}
	usable := func(p string) bool {
		if !filepath.IsAbs(p) || !known[filepath.Base(p)] {
			return false
		}
		st, err := os.Stat(p)
		return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
	}
	if usable(env) {
		return env, nil
	}
	for _, p := range fallbackShells {
		if usable(p) {
			return p, nil
		}
	}
	return "", i18n.E("err.sessionenv.noshell", env)
}
