package deckstore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// n051 承接 n050 评审 4：超额例外消费要写注册表与决定两个文件。两次写之间失败时，
// 存储停止（不再派工），重启后启动中的会话与执行中的决定都标为「结果不明」：名额继续占着、例外不能再用，交给 owner 核对。
func TestExceptionConsumptionWriteFailureRecoversSafely(t *testing.T) {
	for _, failOn := range []string{"decisions.json", "registry.json"} {
		t.Run(failOn, func(t *testing.T) {
			d := testDirs(t)
			env := Envelope{ID: "e", Version: 1, MaxSubagents: 0, MaxFrontier: 0, TokenSoftCap: 1}
			s := withProject(t, d, env)
			req := sub(Economy)
			dec := approvedException(t, s, req, time.Hour)
			writeFault = func(path string) error {
				if filepath.Base(path) == failOn {
					return errors.New("注入：写 " + failOn + " 时断电")
				}
				return nil
			}
			_, err := s.Reserve(req, dec.ID)
			writeFault = nil
			var broken *ErrBroken
			if !errors.As(err, &broken) || s.Broken() == nil {
				t.Fatalf("写失败应让存储停止：%v", err)
			}
			if _, err := s.Reserve(sub(Economy), ""); err == nil {
				t.Fatal("存储停止后不再预留")
			}
			// 重启
			s2, err := Open(d)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := s2.Decision(dec.ID)
			if got.State == StateExecuting || got.State == StateApproved {
				t.Fatalf("重启后例外不能还能用：%s", got.State)
			}
			if _, err := s2.Reserve(req, dec.ID); err == nil {
				t.Fatal("同一个例外不能在重启后再用一次")
			}
			held := false
			for _, x := range s2.Sessions("deck") {
				if x.RequestID == req.RequestID {
					held = true
					if x.Status != StatusUncertain {
						t.Fatalf("写进注册表的那条应是结果不明、继续占名额：%s", x.Status)
					}
				}
			}
			if failOn == "decisions.json" && !held {
				t.Fatal("注册表已落盘的会话应在重启后保留")
			}
			if failOn == "registry.json" && held {
				t.Fatal("注册表没写成就不该有这条会话")
			}
			if failOn == "decisions.json" {
				if u := s2.Slots("deck"); u.Subagents != 1 {
					t.Fatalf("结果不明的会话应继续占名额：%+v", u)
				}
			}
		})
	}
}
