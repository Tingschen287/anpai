package projectctl

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Projection 是 PROJECT-STATUS.md 里能读出来的东西。
//
// 这个文件由 projectctl 的写命令顺带重写，只读的 Work Deck 不会去刷新它。
// 所以它随时可能落后于账本，里面每个数字都要配着 Freshness 一起看。
type Projection struct {
	GeneratedAt Value[time.Time]
	EventCursor string
	Health      Value[string]

	// 节点号到投影里那几张分类表的补充信息。
	Attempts        map[string]int
	Actors          map[string]string
	Evidence        map[string]string
	Reasons         map[string]string
	RequiredActions map[string]string

	// 预算表的四列，形如 5/20。拿不到的维度不会出现在这里。
	BudgetUsed  map[string]float64
	BudgetLimit map[string]float64

	// 投影的三张分类表直接写明了状态的那些节点。
	// 有了它们，这部分节点就不必再逐个去问控制器——真实项目里一次 brief 要两三秒。
	States map[string]State

	// 每个节点最近一次 node_started 的时间，来自投影的「最近事件」表。
	// 那张表只列最近若干条，所以开工较早的节点可能查不到，查不到就是未知。
	StartedAt map[string]time.Time

	// 预算表的表头，解析数据行时用来给每一列对上维度名。
	budgetHeader []string
}

// LoadProjection 解析项目的状态投影。文件不在就返回一个说明原因的空投影。
func LoadProjection(projectRoot string) (Projection, error) {
	path := filepath.Join(projectRoot, "PROJECT-STATUS.md")
	f, err := os.Open(path)
	if err != nil {
		return Projection{
			GeneratedAt: Unknown[time.Time]("读不到状态投影 PROJECT-STATUS.md"),
			Health:      Unknown[string]("读不到状态投影 PROJECT-STATUS.md"),
		}, i18n.E("err.projectctl.status.read", shownPath(path), err)
	}
	defer f.Close()

	p := Projection{
		GeneratedAt:     Unknown[time.Time]("状态投影里没有生成时间"),
		Health:          Unknown[string]("状态投影里没有健康度"),
		Attempts:        map[string]int{},
		Actors:          map[string]string{},
		Evidence:        map[string]string{},
		Reasons:         map[string]string{},
		RequiredActions: map[string]string{},
		BudgetUsed:      map[string]float64{},
		BudgetLimit:     map[string]float64{},
		States:          map[string]State{},
		StartedAt:       map[string]time.Time{},
	}

	section := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := i18n.Neutralize(sc.Text()) // 状态投影是外部原文（返工 4）
		switch {
		case strings.HasPrefix(line, "## "):
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		case strings.HasPrefix(line, "- 生成时间："):
			raw := strings.TrimSpace(strings.TrimPrefix(line, "- 生成时间："))
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				p.GeneratedAt = Known(t)
			} else {
				p.GeneratedAt = Unknown[time.Time]("生成时间无法解析：" + raw)
			}
			continue
		case strings.HasPrefix(line, "- 事件游标："):
			p.EventCursor = strings.TrimSpace(strings.TrimPrefix(line, "- 事件游标："))
			continue
		case strings.HasPrefix(line, "- 总体健康度："):
			p.Health = Known(strings.TrimSpace(strings.TrimPrefix(line, "- 总体健康度：")))
			continue
		}

		cells, ok := tableRow(line)
		if !ok {
			continue
		}
		switch section {
		case "下一可执行节点":
			// 这张表里的节点按定义就是 ready。表为空时占位行写的是 none。
			if len(cells) < 1 || cells[0] == "id" || cells[0] == "none" {
				continue
			}
			p.States[cells[0]] = StateReady
		case "执行中与待评审":
			// id | status | actor | attempt | latest evidence
			if len(cells) < 5 || cells[0] == "id" {
				continue
			}
			id := cells[0]
			p.States[id] = State(cells[1])
			p.Actors[id] = cells[2]
			if n, err := strconv.Atoi(cells[3]); err == nil {
				p.Attempts[id] = n
			}
			p.Evidence[id] = cells[4]
		case "阻塞与失败":
			// id | status | reason | required action
			if len(cells) < 4 || cells[0] == "id" {
				continue
			}
			p.States[cells[0]] = State(cells[1])
			p.Reasons[cells[0]] = cells[2]
			p.RequiredActions[cells[0]] = cells[3]
		case "最近事件":
			// occurred_at | event_id | node | event | actor | evidence
			if len(cells) < 4 || cells[0] == "occurred_at" {
				continue
			}
			if cells[3] != "node_started" {
				continue
			}
			t, err := time.Parse(time.RFC3339, cells[0])
			if err != nil {
				continue
			}
			// 同一节点出现多次时留最新的一次。
			if prev, ok := p.StartedAt[cells[2]]; !ok || t.After(prev) {
				p.StartedAt[cells[2]] = t
			}
		case "预算":
			// 表头给出维度名，数据行给出 已用/上限。
			if len(cells) == 0 {
				continue
			}
			if cells[0] == "worker_rounds" {
				p.budgetHeader = cells
				continue
			}
			if strings.HasPrefix(cells[0], "-") || strings.Contains(cells[0], "---") {
				continue
			}
			for i, cell := range cells {
				if i >= len(p.budgetHeader) {
					break
				}
				used, limit, ok := parseRatio(cell)
				if !ok {
					continue
				}
				name := p.budgetHeader[i]
				p.BudgetUsed[name] = used
				if limit >= 0 {
					p.BudgetLimit[name] = limit
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return p, i18n.E("err.projectctl.status.scan", shownPath(path), err)
	}
	return p, nil
}

// tableRow 把一行 Markdown 表格切成单元格。分隔行返回 false。
func tableRow(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil, false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	parts := strings.Split(inner, "|")
	cells := make([]string, 0, len(parts))
	allDashes := true
	for _, part := range parts {
		c := strings.TrimSpace(part)
		cells = append(cells, c)
		if strings.Trim(c, "-: ") != "" {
			allDashes = false
		}
	}
	if allDashes {
		return nil, false
	}
	return cells, true
}

// parseRatio 解析「已用/上限」。上限写成 not_enforced 时返回 -1 表示没有上限。
func parseRatio(cell string) (used, limit float64, ok bool) {
	parts := strings.SplitN(cell, "/", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	u, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, false
	}
	rawLimit := strings.TrimSpace(parts[1])
	if rawLimit == "not_enforced" {
		return u, -1, true
	}
	l, err := strconv.ParseFloat(rawLimit, 64)
	if err != nil {
		return u, -1, true
	}
	return u, l, true
}
