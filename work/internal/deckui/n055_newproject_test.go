package deckui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

func backspace(m *Settings, n int) {
	for i := 0; i < n; i++ {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		if cmd != nil {
			m.Update(cmd())
		}
	}
}

// tree 建一个目录树：root/ai/{alpha-proj, Beta Proj, .hidden}、root/ai/notes.txt。
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"ai/alpha-proj/src", "ai/Beta Proj", "ai/.hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "ai", "notes.txt"), []byte("x"), 0o644)
	return root
}

// n055：在设置里按 n 选目录建项目，从 deck up 所在的目录开始；id 按目录名生成，回车建好并保存（与 deckd 同一套校验）。
func TestNewProjectFromDirectoryBrowser(t *testing.T) {
	root := tree(t)
	m, api := settingsFixture()
	m.Start = filepath.Join(root, "ai")
	press(m, "n")
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "新建项目 · 选目录") || !strings.Contains(v, filepath.Join(root, "ai")) ||
		!strings.Contains(v, "alpha-proj/") || !strings.Contains(v, "Beta Proj/") || strings.Contains(v, ".hidden") || strings.Contains(v, "notes.txt") {
		t.Fatalf("从起点列出子目录（不含隐藏目录与文件）：\n%s", v)
	}
	// q、s 在表单里不是命令
	press(m, "down", "s", "right") // 进 alpha-proj（s 在浏览时无作用）
	if m.form == nil || m.form.dir != filepath.Join(root, "ai", "alpha-proj") {
		t.Fatalf("应进入 alpha-proj：%+v", m.form)
	}
	press(m, "left") // 回上一级，光标停在 alpha-proj 上
	if m.form.dir != filepath.Join(root, "ai") || m.form.cur != 1 {
		t.Fatalf("回上一级：%s %d", m.form.dir, m.form.cur)
	}
	press(m, "enter") // 在子目录上按回车是进入
	press(m, "up", "enter")
	if m.form.chosen != filepath.Join(root, "ai", "alpha-proj") || m.form.id != "alpha-proj" {
		t.Fatalf("选定目录、按目录名生成 id：%+v", m.form)
	}
	v = ansi.Strip(m.View())
	if !strings.Contains(v, "项目 id  alpha-proj") || !strings.Contains(v, "主 agent  Claude Code · 最强") {
		t.Fatalf("确认页：\n%s", v)
	}
	backspace(m, 5)
	press(m, "-", "x", "Q") // 改 id；大写转小写
	if m.form.id != "alpha-xq" {
		t.Fatalf("改 id：%q", m.form.id)
	}
	saves := len(api.saved)
	press(m, "enter")
	if m.form != nil || len(api.saved) != saves+1 {
		t.Fatalf("回车建好并保存：%+v %d", m.form, len(api.saved)-saves)
	}
	got := api.saved[len(api.saved)-1].Projects
	p := got[len(got)-1]
	if p.ID != "alpha-xq" || p.Name != "alpha-proj" || p.Path != filepath.Join(root, "ai", "alpha-proj") ||
		p.MainAgent != (deckstore.MainAgent{Agent: "claude", Tier: deckstore.Frontier}) || p.Models[deckstore.Frontier].First() != "claude-opus-5-5" ||
		len(p.Agents) != 1 || p.Agents[0] != "claude" || p.Envelope.ID != "alpha-xq-default" {
		t.Fatalf("新项目的默认值：%+v", p)
	}
	if !strings.Contains(m.msg, "按 m 起主 agent") || m.project().ID != "alpha-xq" {
		t.Fatalf("建好后停在新项目上并提示下一步：%q", m.msg)
	}
}

// 不合规的不写盘：目录已是别的项目、id 不合规或重复，都在界面上说明。
func TestNewProjectRejectsBadInput(t *testing.T) {
	root := tree(t)
	m, api := settingsFixture()
	m.set.Projects[0].Path = filepath.Join(root, "ai", "alpha-proj") // 已有项目用了这个目录
	m.Start = filepath.Join(root, "ai")
	press(m, "n", "down", "right", "enter")
	if m.form.chosen != "" || !strings.Contains(m.form.err, "已经是项目") {
		t.Fatalf("目录已是项目：%+v", m.form)
	}
	press(m, "left", "down", "down", "right", "enter") // Beta Proj
	if m.form.id != "beta-proj" {
		t.Fatalf("空格与大写转成合规 id：%q", m.form.id)
	}
	saves := len(api.saved)
	backspace(m, 20)
	press(m, "enter")
	if m.form == nil || m.form.err == "" || len(api.saved) != saves {
		t.Fatalf("空 id 不写盘：%+v", m.form)
	}
	press(m, "d", "e", "c", "k", "enter") // 与已有项目 deck 重复
	if m.form == nil || !strings.Contains(m.form.err, "重复") || len(api.saved) != saves {
		t.Fatalf("重复 id 不写盘：%+v", m.form)
	}
	press(m, "esc", "esc")
	if m.form != nil || len(api.saved) != saves {
		t.Fatal("esc 取消，不写盘")
	}
}

func TestProjectIDFor(t *testing.T) {
	taken := map[string]bool{"wiki": true, "wiki-2": true}
	cases := map[string]string{"/a/My Wiki!": "my-wiki", "/a/wiki": "wiki-3", "/a/中文": "project", "/a/" + strings.Repeat("x", 80): strings.Repeat("x", 60)}
	for dir, want := range cases {
		if got := projectIDFor(dir, taken); got != want {
			t.Errorf("%s → %q，应为 %q", dir, got, want)
		}
	}
}

// 空项目时提示按 n 新建。
func TestSettingsEmptyHintsNew(t *testing.T) {
	m, _ := settingsFixture()
	m.set.Projects = nil
	if v := ansi.Strip(m.View()); !strings.Contains(v, "按 n 新建") || !strings.Contains(v, "n 新建项目") {
		t.Fatalf("\n%s", v)
	}
}

// 自查：经软链进到已有项目的目录，也算重复。
func TestNewProjectSymlinkDuplicate(t *testing.T) {
	root := tree(t)
	link := filepath.Join(root, "ai", "alias")
	if err := os.Symlink(filepath.Join(root, "ai", "alpha-proj"), link); err != nil {
		t.Fatal(err)
	}
	m, _ := settingsFixture()
	m.set.Projects[0].Path = filepath.Join(root, "ai", "alpha-proj")
	m.Start = filepath.Join(root, "ai")
	press(m, "n", "down", "right", "enter") // alias/（排在 alpha-proj 前）
	if m.form.chosen != "" || !strings.Contains(m.form.err, "已经是项目") {
		t.Fatalf("软链指向已有项目的目录：%+v", m.form)
	}
}
