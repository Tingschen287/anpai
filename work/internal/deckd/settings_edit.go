package deckd

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// n057：设置界面能增删全局 agent、改模型与档位、项目每档多选。下面是 deckd 这边的把关。

// checkAgentEdits 核对整份保存里对全局 agent 的改动：
//   - 已有的 agent：工具名、binary、agent 标记、启动额外参数（extra_args，安全白名单）都不许经界面改，
//     只能改显示名与模型清单（deckd 会执行 binary --version、启动核对按 binary 认进程、extra_args 进参数白名单）；
//   - 新加的 agent：只能取自本机工具目录（GET /v1/tools），binary 与 agent 标记照目录，不带 extra_args。
func checkAgentEdits(cur, next deckstore.Settings) error {
	for _, a := range next.Agents {
		old, ok := cur.Agent(a.ID)
		if ok {
			switch {
			case a.Tool != old.Tool:
				return fmt.Errorf("agent %s 的工具名不能在设置界面里改（%s → %s）", a.ID, old.Tool, a.Tool)
			case a.Binary != old.Binary:
				return fmt.Errorf("agent %s 的命令（binary）不能在设置界面里改：要改请手改 agents.yaml", a.ID)
			case a.Agent != old.Agent:
				return fmt.Errorf("agent %s 的 agent 标记不能在设置界面里改：要改请手改 agents.yaml", a.ID)
			case !slices.EqualFunc(a.ExtraArgs, old.ExtraArgs, slices.Equal[[]string]):
				return fmt.Errorf("agent %s 的启动额外参数（extra_args）是安全白名单，不能在设置界面里改：要改请手改 agents.yaml", a.ID)
			}
			continue
		}
		t, ok := CatalogTool(a.Tool)
		switch {
		case !ok:
			return fmt.Errorf("新加的 agent %s 用的工具 %q 不在安排 Anpai 认识的工具里", a.ID, a.Tool)
		case a.Binary != t.Binary || a.Agent != t.Agent:
			return fmt.Errorf("新加的 agent %s 的命令与 agent 标记须照工具目录（%s）", a.ID, t.Binary)
		case len(a.ExtraArgs) > 0:
			return fmt.Errorf("新加的 agent %s 不能带启动额外参数：要加请手改 agents.yaml", a.ID)
		}
	}
	return nil
}

// getResolve 告诉调用方「这一档现在会派哪个模型」（n057）：deck agent 起子 agent 前据此算 args_sha256，
// 与 deckd 派工时的挑法（清单顺序 + 跳过确知用尽的）是同一个函数，算出的指纹才对得上。
func (s *Server) getResolve(w http.ResponseWriter, r *http.Request) {
	c := s.caller(r)
	if !need(w, c, false, RoleOwner, RoleMain) {
		return
	}
	q := r.URL.Query()
	project := q.Get("project")
	if c.Role == RoleMain {
		project = c.Project
	}
	set := s.store.Settings()
	var res deckstore.Resolved
	var err error
	if m := q.Get("model"); m != "" {
		res, err = set.ResolveModel(project, m)
	} else {
		res, err = s.resolveTier(r.Context(), set, project, deckstore.Tier(q.Get("tier")), q.Get("agent"))
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
