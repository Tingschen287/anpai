package projectctl

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// Graph 是 work-graph.yaml 里 Work Deck 用得到的那部分。
//
// 这是静态规格，不是账本：节点清单、标题、依赖和预算上限都写在这里，
// 而每个节点此刻处在什么状态一律不看这个文件，改问 projectctl。
type Graph struct {
	ProjectID     string `yaml:"project_id"`
	GraphRevision int    `yaml:"graph_revision"`
	Policies      struct {
		ProjectBudget struct {
			MaxWorkerRounds  *float64 `yaml:"max_worker_rounds"`
			MaxFrontierCalls *float64 `yaml:"max_frontier_calls"`
			SoftTokenBudget  *float64 `yaml:"soft_token_budget"`
			MaxCostCNY       *float64 `yaml:"max_cost_cny"`
			MaxElapsedHours  *float64 `yaml:"max_elapsed_hours"`
		} `yaml:"project_budget"`
	} `yaml:"policies"`
	Nodes []GraphNode `yaml:"nodes"`
}

// GraphNode 是节点的静态契约部分。
type GraphNode struct {
	ID           string       `yaml:"id"`
	Title        string       `yaml:"title"`
	Kind         string       `yaml:"kind"`
	Risk         string       `yaml:"risk"`
	RouteClass   string       `yaml:"route_class"`
	OwnerRole    string       `yaml:"owner_role"`
	DependsOn    []string     `yaml:"depends_on"`
	ReviewPolicy ReviewPolicy `yaml:"review_policy"`
}

// ReviewPolicy 是节点对评审的要求。
//
// 派评审时必须照着它来：要什么档位、评审者能不能是执行者本人。
// 这些是 Graph 说了算的事，不是派工方随口定的。
type ReviewPolicy struct {
	Required            bool   `yaml:"required"`
	ReviewerRole        string `yaml:"reviewer_role"`
	RouteClass          string `yaml:"route_class"`
	IndependentFromExec bool   `yaml:"independent_from_executor"`
}

// LoadGraph 读项目的 work-graph.yaml。
func LoadGraph(projectRoot string) (Graph, error) {
	path := filepath.Join(projectRoot, "work-graph.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Graph{}, i18n.E("err.projectctl.graph.read", path, err)
	}
	var g Graph
	if err := yaml.Unmarshal(i18n.NeutralizeBytes(data), &g); err != nil { // 项目文件是外部原文（返工 4）
		return Graph{}, i18n.E("err.projectctl.graph.parse", path, err)
	}
	if len(g.Nodes) == 0 {
		return Graph{}, i18n.E("err.projectctl.graph.empty", path)
	}
	return g, nil
}
