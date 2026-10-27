package deckd

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/agentdeck"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/layout"
)

// 客户端在路上断开（F9 里按空格后立刻 Esc 退出）：deckd 给 PanelApplier 的 ctx 被取消，而注册表已经存下。
// 真实的 layoutPanelApplier 拿到已取消的 ctx 会把「没有布局」当真，悄悄返回成功（见 cmd/deck 的 TestAdvApplierCanceledCtxSilentlySkips）。
func TestAdvSetPanelClientDisconnectCancelsApplierCtxAfterSave(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	gotErr := make(chan error, 1)
	f.srv.cfg.PanelApplier = func(ctx context.Context, defs []deckstore.PanelDef) ([]string, []string, error) {
		close(started)
		<-release
		gotErr <- ctx.Err()
		return nil, nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", f.http.URL+"/v1/panels/progress", strings.NewReader(`{"on":true}`))
	req.Header.Set("X-Test-As", "owner")
	done := make(chan struct{})
	go func() { http.DefaultClient.Do(req); close(done) }()
	<-started
	cancel() // 客户端断开
	<-done
	time.Sleep(200 * time.Millisecond)
	close(release)
	err := <-gotErr
	if !onOf(f.store.Panels(), "progress") {
		t.Fatal("夹具：应已存下")
	}
	if err != nil {
		t.Errorf("注册表已存下，应用布局却用了被取消的 ctx：%v（应把「布局收敛」与请求生命周期脱钩，如 context.WithoutCancel）", err)
	}
}

// 请求体缺 on：`{}` 被当成 on=false，静默把面板拔掉。
func TestAdvSetPanelEmptyBodyTurnsPanelOff(t *testing.T) {
	f := newFixture(t)
	code, body := f.do("owner", "POST", "/v1/panels/quota", `{}`)
	t.Logf("%d %s", code, body)
	if code == 200 && !onOf(f.store.Panels(), "quota") {
		t.Errorf("POST {} 把额度面板拔掉了（on 缺省 = false）；应要求 on 必填（*bool）返回 400")
	}
	// 其它畸形体
	for _, b := range []string{``, `null`, `{"on":null}`, `{"on":"true"}`, `{"on":1}`, `[]`, `{"on":true}{"on":false}`} {
		code, body := f.do("owner", "POST", "/v1/panels/decisions", b)
		t.Logf("body=%q → %d %.80s; decisions on=%v", b, code, body, onOf(f.store.Panels(), "decisions"))
	}
}

// panels.yaml 在 deckd 运行期间被手改，再经 F9 挂拔任意一块：手改被整份覆盖；GET 也看不到手改（页面注释说「手改过的 panels.yaml 都能跟上」）。
func TestAdvHandEditedPanelsYamlIsOverwrittenAndInvisible(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.dirs.Config, "panels.yaml")
	edited := "panels:\n  - name: decisions\n    command: deck panel decisions\n    region: right\n    on: true\n  - name: mine\n    command: htop\n    region: bottom\n    on: true\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := f.do("owner", "GET", "/v1/panels", "")
	if strings.Contains(body, `"mine"`) {
		t.Log("GET 能看到手改（好）")
	} else {
		t.Errorf("GET /v1/panels 看不到手改过的 panels.yaml（deckd 只在启动时读一次）：%.200s", body)
	}
	f.do("owner", "POST", "/v1/panels/quota", `{"on":false}`)
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "mine") {
		t.Errorf("手改的自定义面板 mine 被整份覆盖丢掉了：\n%s", data)
	}
}

// panels.yaml 坏掉时的启动行为：整个存储打不开（deckd/deck up 起不来）。逐个畸形输入记下结果。
func TestAdvBrokenPanelsYamlOpenBehaviour(t *testing.T) {
	cases := map[string]string{
		"垃圾":          "panels: [ {name: a\n",
		"缺 name":      "panels:\n  - command: x\n    region: right\n    on: true\n",
		"缺 command":   "panels:\n  - name: a\n    region: right\n",
		"缺 region":    "panels:\n  - name: a\n    command: x\n",
		"重名":          "panels:\n  - {name: a, command: x, region: right}\n  - {name: a, command: x, region: bottom}\n",
		"region=left": "panels:\n  - {name: a, command: x, region: left}\n",
		"名字含冒号":       "panels:\n  - {name: \"a:b\", command: x, region: right}\n",
		"名字含引号":       "panels:\n  - {name: \"a'b\", command: x, region: right}\n",
		"名字大写":        "panels:\n  - {name: Quota, command: x, region: right}\n",
		"名字换行":        "panels:\n  - {name: \"a\\nb\", command: x, region: right}\n",
		"名字65字符":      "panels:\n  - {name: " + strings.Repeat("a", 65) + ", command: x, region: right}\n",
		"空文件":         "",
		"panels 空":    "panels: []\n",
		"panels null": "panels:\n",
		"顶层是列表":       "- a\n- b\n",
		"未知字段":        "panels:\n  - {name: a, command: x, region: right, bogus: 1}\n",
		"on 是字符串":     "panels:\n  - {name: a, command: x, region: right, on: maybe}\n",
	}
	for name, content := range cases {
		root := t.TempDir()
		dirs := deckstore.Dirs{Config: filepath.Join(root, "cfg"), State: filepath.Join(root, "state")}
		os.MkdirAll(dirs.Config, 0o700)
		os.WriteFile(filepath.Join(dirs.Config, "panels.yaml"), []byte(content), 0o600)
		st, err := deckstore.Open(dirs)
		if err != nil {
			t.Logf("%-12s → Open 失败：%.90v", name, err)
			continue
		}
		t.Logf("%-12s → Open 成功，面板 %+v", name, st.Panels())
		// 打开成功的：POST 挂拔不能崩
		st.SetPanel("a", true)
	}
}

// 12 个并发 POST（真 tmux + 真 flock 锁 + 真 ApplyPanels）：最终布局与存储一致，没有重复窗格。
func TestAdvConcurrentPostsRealLayoutConverges(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("没有 tmux")
	}
	f := newFixture(t)
	dir, _ := os.MkdirTemp("", "advd")
	sock := filepath.Join(dir, "deck")
	t.Cleanup(func() {
		c := exec.Command("tmux", "-S", sock, "kill-server")
		c.Env = append(os.Environ(), "TMUX=")
		c.Run()
		os.RemoveAll(dir)
	})
	fake := filepath.Join(dir, "deck.sh")
	os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755)
	s := layout.Spec{Deck: fake, LayoutSocket: sock, AgentSocket: filepath.Join(dir, "agent"), Width: 160, Height: 40, Panels: f.store.Panels()}
	conf := filepath.Join(dir, "base.conf")
	os.WriteFile(conf, []byte(layout.BaseConf(s)), 0o600)
	m := layout.TmuxMux{Socket: sock}
	if _, err := layout.Build(m, s, conf); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.PanelApplier = func(ctx context.Context, defs []deckstore.PanelDef) ([]string, []string, error) {
		unlock, err := layout.Lock(sock+".lock", 10*time.Second)
		if err != nil {
			return nil, nil, err
		}
		defer unlock()
		res, err := layout.ApplyPanels(m, s, defs)
		return res.Attached, res.Removed, err
	}
	var wg sync.WaitGroup
	var errs sync.Map
	names := []string{"decisions", "quota", "progress", "activity", "notifications"}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			on := "true"
			if (i/5)%2 == 0 {
				on = "false"
			}
			code, body := f.do("owner", "POST", "/v1/panels/"+names[i%5], `{"on":`+on+`}`)
			var out panelBodyOut
			json.Unmarshal([]byte(body), &out)
			if code != 200 || !out.Applied {
				errs.Store(i, body)
			}
		}(i)
	}
	wg.Wait()
	errs.Range(func(k, v any) bool { t.Errorf("请求 %v：%v", k, v); return true })
	ps, _ := agentdeck.LayoutPanes(context.Background(), sock)
	have := map[string]int{}
	for _, p := range ps {
		if strings.HasPrefix(p.Role, "panel:") {
			have[p.Role[strings.LastIndex(p.Role, ":")+1:]]++
		}
	}
	for _, d := range f.store.Panels() {
		want := 0
		if d.On {
			want = 1
		}
		if have[d.Name] != want {
			t.Errorf("面板 %s：存储 on=%v，布局里 %d 个窗格", d.Name, d.On, have[d.Name])
		}
	}
	t.Logf("布局：%v", have)
}

// 手改坏了 panels.yaml（deckd 运行期间）再挂拔：不能把坏文件整份覆盖掉（owner 还要修它），应报错。
func TestAdvBrokenHandEditNotClobbered(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.dirs.Config, "panels.yaml")
	broken := "panels: [ {name: a\n"
	os.WriteFile(path, []byte(broken), 0o600)
	code, body := f.do("owner", "POST", "/v1/panels/quota", `{"on":false}`)
	data, _ := os.ReadFile(path)
	t.Logf("%d %.120s", code, body)
	if string(data) != broken {
		t.Errorf("手改坏的 panels.yaml 被覆盖了：\n%s", data)
	}
	if code < 400 {
		t.Errorf("应报错而不是 %d", code)
	}
}
