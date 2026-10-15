package deckui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 设置界面里新建项目（n055）：按 n 打开目录浏览，从 deck up 所在的目录开始；选好目录后确认项目 id（按目录名生成，可改），
// 回车即建好，随即自动保存（n059）。agent、三档模型、主 agent 先按全局清单给默认值，之后在「项目」页里改。

// projectForm 是新建项目的表单。
type projectForm struct {
	dir     string   // 正在浏览的目录
	subdirs []string // dir 下的子目录（不含隐藏目录）
	cur     int      // 0 是「用这个目录」，其余是 subdirs[cur-1]
	scroll  int
	chosen  string // 选好的目录；空表示还在浏览
	id      string // 项目 id（可改）
	err     string
}

func newProjectForm(start string) *projectForm {
	f := &projectForm{}
	f.enter(start)
	return f
}

// enter 进入一个目录：读它的子目录。读不了就留在原地并说明。
func (f *projectForm) enter(dir string) {
	dir = filepath.Clean(dir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		f.err = T("form.cantread", dir, errText(err))
		if f.dir == "" { // 起点就读不了：从家目录开始
			if home, herr := os.UserHomeDir(); herr == nil && home != dir {
				f.enter(home)
			}
		}
		return
	}
	var subs []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			subs = append(subs, e.Name())
			continue
		}
		if e.Type()&os.ModeSymlink != 0 { // 指向目录的软链也能进
			if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
				subs = append(subs, e.Name())
			}
		}
	}
	sort.Slice(subs, func(i, j int) bool { return strings.ToLower(subs[i]) < strings.ToLower(subs[j]) }) // 不分大小写
	f.dir, f.subdirs, f.cur, f.scroll, f.err = dir, subs, 0, 0, ""
}

var idUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// projectIDFor 按目录名生成项目 id（小写字母数字、-、_，不超过 64），与已有的重复就加 -2、-3……
func projectIDFor(dir string, taken map[string]bool) string {
	base := strings.Trim(idUnsafe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-_")
	if base == "" {
		base = "project"
	}
	if len(base) > 60 {
		base = strings.TrimRight(base[:60], "-_")
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// defaultProject 给新项目填默认值：勾选全局清单里所有已装的 agent（都没装就全勾），主 agent 取第一个已装的 agent 的
// frontier（没有 frontier 模型就取它有的最高一档），三档模型各取主 agent 在该档的第一个模型、没有就取其他勾选 agent 的。
func (m *Settings) defaultProject(id, dir string) deckstore.ProjectDef {
	p := deckstore.ProjectDef{ID: id, Name: filepath.Base(dir), Path: dir, Models: map[deckstore.Tier]deckstore.ModelList{},
		Envelope: deckstore.DefaultEnvelope(id)}
	for _, a := range m.set.Agents {
		if m.installed(a) {
			p.Agents = append(p.Agents, a.ID)
		}
	}
	if len(p.Agents) == 0 {
		for _, a := range m.set.Agents {
			p.Agents = append(p.Agents, a.ID)
		}
	}
	if len(p.Agents) == 0 {
		return p
	}
	main, _ := m.agent(p.Agents[0])
	for i := len(deckstore.Tiers) - 1; i >= 0; i-- {
		t := deckstore.Tiers[i]
		for _, md := range main.Models {
			if md.Tier == t && len(p.Models[t]) == 0 {
				p.Models[t] = deckstore.ModelList{md.ID}
			}
		}
		if len(p.Models[t]) == 0 {
			if opts := m.modelsFor(&p, t); len(opts) > 0 {
				p.Models[t] = deckstore.ModelList{opts[0]}
			}
		}
		if p.MainAgent.Agent == "" && len(p.Models[t]) > 0 && hasTier(main, t) {
			p.MainAgent = deckstore.MainAgent{Agent: main.ID, Tier: t}
		}
	}
	return p
}

func hasTier(a deckstore.AgentDef, t deckstore.Tier) bool {
	for _, md := range a.Models {
		if md.Tier == t {
			return true
		}
	}
	return false
}

// formKey 处理表单打开时的按键（全部按键都归表单，免得 q、s、d 被当成命令）。返回要执行的命令。
func (m *Settings) formKey(msg tea.KeyMsg) tea.Cmd {
	f := m.form
	k := msg.String()
	if f.chosen == "" { // 浏览目录
		switch k {
		case "esc", "q", "ctrl+c":
			m.form, m.msg = nil, T("form.cancel")
		case "up", "k":
			f.cur = max(0, f.cur-1)
		case "down", "j":
			f.cur = min(len(f.subdirs), f.cur+1)
		case "left", "h", "backspace":
			if parent := filepath.Dir(f.dir); parent != f.dir {
				from := filepath.Base(f.dir)
				f.enter(parent)
				for i, s := range f.subdirs { // 回到上一级时光标停在刚才那个目录上
					if s == from {
						f.cur = i + 1
					}
				}
			}
		case "right", "l":
			if f.cur > 0 {
				f.enter(filepath.Join(f.dir, f.subdirs[f.cur-1]))
			}
		case "enter", " ":
			if f.cur > 0 {
				f.enter(filepath.Join(f.dir, f.subdirs[f.cur-1]))
				return nil
			}
			m.choose(f.dir)
		}
		return nil
	}
	switch k { // 确认项目 id
	case "esc", "ctrl+c":
		f.chosen, f.err = "", "" // 回到目录浏览
	case "enter":
		return m.createProject()
	case "backspace":
		if r := []rune(f.id); len(r) > 0 {
			f.id = string(r[:len(r)-1])
		}
	default:
		if msg.Type == tea.KeyRunes {
			f.id += strings.ToLower(string(msg.Runes))
			if r := []rune(f.id); len(r) > 64 { // 按字符截，不切出半个汉字
				f.id = string(r[:64])
			}
		}
	}
	f.err = ""
	return nil
}

// choose 选定目录，进入确认 id 这一步。目录已经是某个项目的就说明，不往下走。
func (m *Settings) choose(dir string) {
	f := m.form
	if err := deckstore.CheckPath(dir); err != nil {
		f.err = T("form.badpath", errText(err))
		return
	}
	taken := map[string]bool{}
	real := func(p string) string { // 经软链进来的同一个目录也算重复（自查）
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	for _, p := range m.set.Projects {
		if real(p.Path) == real(dir) {
			f.err = T("form.taken", sanitize(p.Name))
			return
		}
		taken[p.ID] = true
	}
	f.chosen, f.id, f.err = dir, projectIDFor(dir, taken), ""
}

// createProject 按表单建项目：先在本地按 deckd 的同一套规则校验（不合规就说明、不写盘），通过后由自动保存整份保存。
func (m *Settings) createProject() tea.Cmd {
	f := m.form
	if st, err := os.Stat(f.chosen); err != nil || !st.IsDir() {
		f.err = T("form.gone", f.chosen)
		return nil
	}
	next, err := cloneSettings(m.set)
	if err != nil {
		f.err = errText(err)
		return nil
	}
	p := m.defaultProject(f.id, f.chosen)
	next.Projects = append(next.Projects, p)
	if err := next.Validate(); err != nil {
		f.err = T("form.invalid", errText(err))
		return nil
	}
	m.set = next
	m.switchPage(pageProject) // 先换页（各页光标各自记着），再把光标放到新项目第一行（n059 自查）
	m.proj, m.cur = len(m.set.Projects)-1, 0
	m.form = nil
	m.touch() // Update 看到改动就自动保存（n059）
	m.msg = T("form.created", sanitize(p.Name))
	m.hint, m.hintEdits = T("form.next", sanitize(p.Name)), m.edits // 存上了含这个项目的那次保存才提示去侧栏
	return nil
}

// formView 画表单。
func (m *Settings) formView() string {
	f := m.form
	var b strings.Builder
	if f.chosen == "" {
		b.WriteString(m.titleLine(" "+T("form.browse.title"), T("form.browse.keys")))
		fmt.Fprintf(&b, " \x1b[1m%s\x1b[0m\n\n", sanitize(f.dir))
		rows := []string{green + T("form.usethis") + reset}
		for _, s := range f.subdirs {
			rows = append(rows, sanitize(s)+"/")
		}
		room := 20
		if m.height > 10 {
			room = m.height - 8
		}
		if f.cur < f.scroll {
			f.scroll = f.cur
		}
		if f.cur >= f.scroll+room {
			f.scroll = f.cur - room + 1
		}
		for i := f.scroll; i < len(rows) && i < f.scroll+room; i++ {
			mark := " "
			if i == f.cur {
				mark = "\x1b[38;2;180;245;70m›\x1b[0m"
			}
			fmt.Fprintf(&b, " %s %s\n", mark, rows[i])
		}
		if len(f.subdirs) == 0 {
			b.WriteString(dimOn + "   " + T("form.nosub") + reset + "\n")
		}
	} else {
		b.WriteString(m.titleLine(" "+T("form.confirm.title"), T("form.confirm.keys")))
		labels := []string{T("form.dir"), T("form.id"), T("form.agents"), T("form.main")}
		lw := 0
		for _, l := range labels {
			lw = max(lw, ansi.StringWidth(l))
		}
		label := func(l string) string { return l + strings.Repeat(" ", lw-ansi.StringWidth(l)) } // 按显示宽度对齐（n059 自查）
		fmt.Fprintf(&b, "   %s  %s\n", label(T("form.dir")), sanitize(f.chosen))
		fmt.Fprintf(&b, "   %s  \x1b[4m%s\x1b[0m▏\n", label(T("form.id")), sanitize(f.id))
		p := m.defaultProject(f.id, f.chosen)
		main := dimOn + T("form.noagent") + reset
		if p.MainAgent.Agent != "" {
			main = sanitize(m.mainText(p.MainAgent))
		}
		var names []string
		for _, id := range p.Agents {
			names = append(names, m.agentName(id))
		}
		fmt.Fprintf(&b, "   %s  %s\n   %s  %s\n", label(T("form.agents")), sanitize(strings.Join(names, T("sep.list"))), label(T("form.main")), main)
		b.WriteString(" " + m.note(T("form.later")))
	}
	if f.err != "" {
		b.WriteString("\n\x1b[38;5;196m " + oneLine(deckstore.Clean(f.err, 240)) + "\x1b[0m\n")
	}
	return b.String()
}
