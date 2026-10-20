package deckstore

import (
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 返工 4：老注册表里系统生成的主 agent 标题补码。只认确切形状。
func TestN060BackfillMainTitleCode(t *testing.T) {
	set := Settings{Projects: []ProjectDef{{ID: "p1", Name: "Alpha"}, {ID: "p2", Name: ""}}}
	cases := []struct {
		name string
		x    Session
		want bool
	}{
		{"按项目名拼的", Session{Project: "p1", Role: "main", Title: "主 agent · Alpha"}, true},
		{"项目没有名字时用编号", Session{Project: "p2", Role: "main", Title: "主 agent · p2"}, true},
		{"已经有码", Session{Project: "p1", Role: "main", Title: "主 agent · Alpha", TitleCode: "x"}, false},
		{"子 agent 同样的标题不碰", Session{Project: "p1", Role: "sub", Title: "主 agent · Alpha"}, false},
		{"owner 改过的", Session{Project: "p1", Role: "main", Title: "我的主会话"}, false},
		{"别的项目的名字", Session{Project: "p1", Role: "main", Title: "主 agent · Beta"}, false},
		{"多了几个字", Session{Project: "p1", Role: "main", Title: "主 agent · Alpha 备用"}, false},
		{"项目已删除", Session{Project: "gone", Role: "main", Title: "主 agent · Alpha"}, false},
	}
	for _, c := range cases {
		x := c.x
		orig := x.Title
		if got := backfillMainTitleCode(&x, set); got != c.want {
			t.Errorf("%s：补码=%v，期望 %v", c.name, got, c.want)
		}
		if x.Title != orig {
			t.Errorf("%s：中文 title 被改了：%q", c.name, x.Title)
		}
		if c.want {
			if x.TitleCode != "err.deckd.serverpostmain.4" || len(x.TitleArgs) != 1 {
				t.Errorf("%s：码不对：%q %v", c.name, x.TitleCode, x.TitleArgs)
			}
			if got := i18n.E(i18n.Key(x.TitleCode), []any(x.TitleArgs)...).Error(); got != orig {
				t.Errorf("%s：码重新拼出来的中文和 title 不一样：%q != %q", c.name, got, orig)
			}
		}
	}
}
