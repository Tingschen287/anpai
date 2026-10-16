package viewstate

import "testing"

// n055：主 agent 的 CLI 停在启动确认上（AwaitingOwner）时，显示在中间就对 owner 放开输入，由 owner 自己选；
// 第一条 brief 没送达、也不在等确认的主 agent 仍只读。OpenMainInput 按最新状态放开（不在中间就不动）。
func TestAwaitingOwnerMainIsWritable(t *testing.T) {
	w := newWorld(t)
	if err := w.m.Open("fresh"); err != nil {
		t.Fatal(err)
	}
	if !w.c.ro {
		t.Fatal("brief 没送达、也不在等确认：只读")
	}
	f := w.targets["fresh"]
	f.AwaitingOwner = true
	w.targets["fresh"] = f
	if err := w.m.OpenMainInput("fresh"); err != nil || w.c.ro {
		t.Fatalf("等 owner 处理启动确认：放开输入：%v ro=%v", err, w.c.ro)
	}
	// 切走再切回：按目标状态放开
	if err := w.m.Open("subA"); err != nil || !w.c.ro {
		t.Fatalf("切到子 agent 只读：%v", err)
	}
	if err := w.m.Focus("fresh"); err != nil || w.c.ro {
		t.Fatalf("切回等确认的主 agent 放开：%v ro=%v", err, w.c.ro)
	}
	w.noViolations()
	// 不在中间：OpenMainInput 什么都不动
	if err := w.m.Focus("subA"); err != nil {
		t.Fatal(err)
	}
	if err := w.m.OpenMainInput("fresh"); err != nil || !w.c.ro {
		t.Fatalf("不在中间不放开：%v ro=%v", err, w.c.ro)
	}
	// 子 agent 不管什么状态都不会经 OpenMainInput 放开
	a := w.targets["subA"]
	a.AwaitingOwner = true
	w.targets["subA"] = a
	if err := w.m.OpenMainInput("subA"); err != nil || !w.c.ro {
		t.Fatalf("子 agent 不放开：%v ro=%v", err, w.c.ro)
	}
}
