package wirecheck

import (
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Exception 是一条例外：某个字段确实不能是码，里面的中文是外部原文，或只给模型和记账。
// 按「方法 路径 · 字段路径 · 产生函数」登记，写理由；不许整包豁免。
type Exception struct {
	Pkg   string // 在哪个包的测试里应该用到它（用来查过期）
	Kind  string // 方法 + 路径，路径里的 * 是一段；通知用 notification
	Path  string // 字段路径，数组下标写成 []
	Func  string // 产生它的函数
	Why   string // 理由
	Never bool   // 为真：这条例外只在个别测试里出现，不检查过期
}

func (e Exception) key() string { return e.Kind + " · " + e.Path + " · " + e.Func }

// allowed 查例外。命中就记一次使用。
func (c *checker) allowed(p string) bool {
	np := normPath(p)
	method, urlPath := c.kind, ""
	if i := strings.IndexByte(c.kind, ' '); i > 0 {
		method = c.kind[:i]
		rest := c.kind[i+1:]
		if j := strings.IndexByte(rest, ' '); j > 0 {
			rest = rest[:j]
		}
		urlPath = rest
	}
	for _, e := range Exceptions {
		if e.Path != np {
			continue
		}
		ek, ep := e.Kind, ""
		if i := strings.IndexByte(ek, ' '); i > 0 {
			ep = ek[i+1:]
			ek = ek[:i]
		}
		if ek != "*" && ek != method {
			continue
		}
		if ep != "" {
			if ok, _ := path.Match(ep, urlPath); !ok {
				continue
			}
		}
		st.mu.Lock()
		st.used[e.key()]++
		st.mu.Unlock()
		return true
	}
	return false
}

// Run 是 TestMain 的主体：打开钩子，跑 m.Run，有命中或例外过期就以非零退出。
//
//	func TestMain(m *testing.M) { os.Exit(wirecheck.Run("deckd", m)) }
//
// 环境变量：
//   - N060_TEST_LANG=en：把进程语言设成英文再跑，看现有测试在英文模式下的表现；
//   - N060_WIRECHECK_REPORT=<文件>：把本包的统计追加写进这个文件（go test 不加 -v 时看不到 stderr）。
//
// 钩子每次测试运行都开着，而不是靠环境变量开：免得被人忘了开，守卫就成了摆设。
func Run(pkg string, m *testing.M) int {
	if os.Getenv("N060_TEST_LANG") == "en" {
		i18n.SetLang(i18n.EN)
	}
	i18n.WireHook = func(kind string, doc []byte) {
		if strings.HasPrefix(kind, "request ") {
			requestBody(doc)
			return
		}
		if kind == "notification" {
			NotificationLine(doc)
			return
		}
		Doc(kind, doc)
	}
	i18n.ArgHook = Args
	code := m.Run()
	// 钩子不撤：测试结束后后台 goroutine 可能还在写，撤钩子会和它们抢；进程马上就退出了。

	snap := Snapshot()
	hits := Hits()
	var stale []string
	// 只在跑全包测试时查过期：用 -run 只跑几个测试，没用到的例外不算过期。
	whole := true
	if f := flag.Lookup("test.run"); f != nil && f.Value.String() != "" {
		whole = false
	}
	for _, e := range Exceptions {
		if whole && e.Pkg == pkg && !e.Never && snap.AllowUsed[e.key()] == 0 {
			stale = append(stale, e.key())
		}
	}
	sort.Strings(stale)
	line := fmt.Sprintf("wirecheck pkg=%s lang=%s 响应和SSE事件=%d 落盘通知=%d 带码错误=%d 来自测试数=%d 命中=%d 例外用到=%d/%d 过期例外=%d",
		pkg, i18n.Lang(), snap.Docs, snap.Notes, snap.Errs, snap.Tests, len(hits), usedCount(pkg, snap), countFor(pkg), len(stale))
	if f := os.Getenv("N060_WIRECHECK_REPORT"); f != "" {
		if fh, err := os.OpenFile(f, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(fh, line)
			fh.Close()
		}
	}
	if len(hits) > 0 || len(stale) > 0 {
		fmt.Fprintln(os.Stderr, line)
		sort.Slice(hits, func(i, j int) bool { return hits[i].String() < hits[j].String() })
		seen := map[string]bool{}
		for _, h := range hits {
			if s := h.String(); !seen[s] {
				seen[s] = true
				fmt.Fprintln(os.Stderr, "wirecheck 命中：", s)
			}
		}
		for _, s := range stale {
			fmt.Fprintln(os.Stderr, "wirecheck 例外过期（本包测试里没有再用到）：", s)
		}
		if code == 0 {
			code = 1
		}
		return code
	}
	if testing.Verbose() {
		fmt.Fprintln(os.Stderr, line)
	}
	return code
}

func countFor(pkg string) int {
	n := 0
	for _, e := range Exceptions {
		if e.Pkg == pkg && !e.Never {
			n++
		}
	}
	return n
}

func usedCount(pkg string, s Stats) int {
	n := 0
	for _, e := range Exceptions {
		if e.Pkg == pkg && !e.Never && s.AllowUsed[e.key()] > 0 {
			n++
		}
	}
	return n
}
