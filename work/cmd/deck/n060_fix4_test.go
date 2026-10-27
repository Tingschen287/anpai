package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"unicode"
	"unsafe"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4 第 2 条：deck center 自己画在中间窗格里的提示按界面语言显示。
// 只注入内存 HTTP transport（不监听 socket、不连 tmux、不起 deckd）；fence 本身是生产代码。

type memTransport func(*http.Request) (*http.Response, error)

func (fn memTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// memClient 造一个走内存 transport 的真实 deckclient.Client。
// Client 没有注入 transport 的口子，这里用反射改它的 hc 字段，只在测试里。
func memClient(t *testing.T, sock string, rt http.RoundTripper) *deckclient.Client {
	t.Helper()
	c := deckclient.NewUI(sock)
	v := reflect.ValueOf(c).Elem().FieldByName("hc")
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(&http.Client{Transport: rt}))
	return c
}

func hasHanText(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			return true
		}
	}
	return false
}

// fenceCase 是栅栏没确认的一种原因。first 是第一次请求的结果，之后一律确认。
type fenceCase struct {
	name  string
	first func(*http.Request) (*http.Response, error)
	// setup 准备状态目录和锁，返回 socket 路径
	setup func(t *testing.T) string
	// oldWhy 是 a80bddc 里这个原因的中文原文（err 是同一次请求的错误）
	oldWhy func(lock string, perr, err error) string
}

func fenceCases() []fenceCase {
	dial := func(*http.Request) (*http.Response, error) { return nil, errors.New("boom") }
	dirSock := func(t *testing.T) string { return filepath.Join(t.TempDir(), "deckd.sock") }
	return []fenceCase{
		{"unconfirmed", func(*http.Request) (*http.Response, error) { return jsonResp(200, `{"fenced":false}`), nil }, dirSock,
			func(string, error, error) string { return "deckd 的回答没有确认（fenced 不是 true）" }},
		{"notup", dial, dirSock,
			func(lock string, _, err error) string {
				return fmt.Sprintf("deckd 还没起来（%s 不存在）（%v）", lock, err)
			}},
		{"lockheld", dial, func(t *testing.T) string {
			sock := dirSock(t)
			f, err := os.OpenFile(filepath.Join(filepath.Dir(sock), "deckd.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			return sock
		}, func(_ string, _, err error) string {
			return fmt.Sprintf("deckd 的单实例锁还被持有（可能正在关闭、手上还有请求）（%v）", err)
		}},
		{"probefail", dial, func(t *testing.T) string {
			// 状态目录的位置其实是个普通文件：打开锁文件得到 ENOTDIR，不是「不存在」
			file := filepath.Join(t.TempDir(), "notadir")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(file, "deckd.sock")
		}, func(_ string, perr, err error) string {
			return fmt.Sprintf("探测 deckd 单实例锁失败：%v（%v）", perr, err)
		}},
		{"coded-http-error", func(*http.Request) (*http.Response, error) {
			return jsonResp(503, `{"error":"测试用的原因","code":"err.deckd.serverpostreport.1"}`), nil
		}, func(t *testing.T) string {
			sock := dirSock(t)
			f, err := os.OpenFile(filepath.Join(filepath.Dir(sock), "deckd.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			return sock
		}, func(_ string, _, err error) string {
			return fmt.Sprintf("deckd 的单实例锁还被持有（可能正在关闭、手上还有请求）（%v）", err)
		}},
	}
}

func runFence(t *testing.T, tc fenceCase, lang string) (shown []string, lock string) {
	t.Helper()
	shortFence(t)
	i18n.SetLang(lang)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	sock := tc.setup(t)
	calls := 0
	rt := memTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return tc.first(r)
		}
		return jsonResp(200, `{"fenced":true}`), nil
	})
	c := memClient(t, sock, rt)
	fence(c, filepath.Join(filepath.Dir(sock), "center.log"), func(s string) { shown = append(shown, s) })
	return shown, deckd.LockPathForSocket(sock)
}

// Codex 复审 3 的反例（TestAuditN060R4FenceVisibleChinese）：英文下调用真实 fence，画出来的字不含汉字。
func TestN060R4FenceEnglishHasNoHan(t *testing.T) {
	for _, tc := range fenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			shown, _ := runFence(t, tc, i18n.EN)
			if len(shown) != 1 {
				t.Fatalf("应当只画一次（第二次就确认了）：%q", shown)
			}
			t.Logf("EN: %s", shown[0])
			if hasHanText(shown[0]) {
				t.Errorf("英文中间窗格里还有汉字：%q", shown[0])
			}
			if !strings.HasPrefix(shown[0], "Waiting for deckd") {
				t.Errorf("英文句子不对：%q", shown[0])
			}
		})
	}
}

// 中文显示与 a80bddc 逐字一致：用 a80bddc 的拼法重算期望值。
func TestN060R4FenceChineseIsUnchanged(t *testing.T) {
	for _, tc := range fenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			shown, lock := runFence(t, tc, i18n.ZH)
			if len(shown) != 1 {
				t.Fatalf("应当只画一次：%q", shown)
			}
			// 重现 a80bddc 的 err 与 perr
			var err, perr error
			switch tc.name {
			case "unconfirmed":
			case "coded-http-error":
				err = &deckclient.Error{Status: 503, Message: "测试用的原因", Code: "err.deckd.serverpostreport.1"}
			default:
				err = i18n.E("err.deckclient.dial", fmt.Errorf(`Post "http://deckd/v1/view": boom`))
			}
			if tc.name == "probefail" {
				_, perr = deckd.ProbeLock(lock)
			}
			want := "等 deckd 确认没有进行中的操作再重连中间窗格…（" + deckstore.Display(tc.oldWhy(lock, perr, err), 160) + "）"
			if shown[0] != want {
				t.Errorf("中文变了：\n现在 %q\n原来 %q", shown[0], want)
			}
		})
	}
}

// 写进 center.log 的仍是中文原文。
func TestN060R4FenceLogStaysChinese(t *testing.T) {
	tc := fenceCases()[0]
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	shortFence(t)
	sock := tc.setup(t)
	calls := 0
	c := memClient(t, sock, memTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return tc.first(r)
		}
		return jsonResp(200, `{"fenced":true}`), nil
	}))
	log := filepath.Join(filepath.Dir(sock), "center.log")
	fence(c, log, func(string) {})
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "重连前等 deckd 确认没有进行中的操作，还没确认：deckd 的回答没有确认（fenced 不是 true）（不起新 client，每秒再试）") {
		t.Fatalf("日志应是中文原文：%q", data)
	}
}

// Pause 的说明和 tmux 退出说明的包裹：中文逐字不变，英文没有汉字。
func TestN060R4PauseHintAndAttachExit(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.ZH)
	if got, want := i18n.T("center.pause.hint", "/tmp/s"), "检查 agent-deck 的 tmux server（/tmp/s）后按回车重试，输入 q 回车退出。"; got != want {
		t.Fatalf("%q != %q", got, want)
	}
	inner := errors.New("exit status 1")
	e := i18n.E("err.deck.center.attachexit", inner, "no server running")
	if got, want := e.Error(), fmt.Errorf("%w（%s）", inner, "no server running").Error(); got != want {
		t.Fatalf("Error() 变了：%q != %q", got, want)
	}
	if !errors.Is(e, inner) {
		t.Fatal("包裹后 errors.Is 找不到原错误")
	}
	// 整屏文字：中文逐字等于 a80bddc 的拼法
	if got, want := pauseScreen("原因", "/tmp/s"), "\x1b[2J\x1b[H"+"\n  原因\n\n  检查 agent-deck 的 tmux server（/tmp/s）后按回车重试，输入 q 回车退出。\n"; got != want {
		t.Fatalf("Pause 的整屏文字变了：%q != %q", got, want)
	}
	i18n.SetLang(i18n.EN)
	if got := pauseScreen("reason", "/tmp/s"); hasHanText(got) {
		t.Fatalf("英文 Pause 画面里有汉字：%q", got)
	}
	if got := deckclient.Show(e); hasHanText(got) || !strings.Contains(got, "exit status 1 (no server running)") {
		t.Fatalf("英文退出说明：%q", got)
	}
}
