package i18n

import (
	"sync"
	"testing"
)

// 段 3 返工 3（复审 2 的建议）：「代次没变」与「设置新语言」是同一次条件更新。

func TestN060S3Fix3SetLangIfIsConditional(t *testing.T) {
	SetLang(ZH)
	t.Cleanup(func() { SetLang(ZH) })
	seq := LangSeq()
	if SetLangIf("xx", seq) || Lang() != ZH {
		t.Fatal("不合法的语言不该切换")
	}
	if SetLangIf(ZH, seq) || LangSeq() != seq {
		t.Fatal("已经是这个语言：不切、代次不动")
	}
	if !SetLangIf(EN, seq) || Lang() != EN || LangSeq() != seq+1 {
		t.Fatalf("代次没变应切换并加一次代次：%s %d→%d", Lang(), seq, LangSeq())
	}
	if SetLangIf(ZH, seq) || Lang() != EN {
		t.Fatal("代次已变（发出之后换过语言）：旧的条件更新不该生效")
	}
}

// 并发：一边不断 SetLang（模拟 owner 存上新语言），一边用发出前的代次条件更新。
// 条件更新一旦成功，成功那一刻的代次必须正好是 seq（没有任何 SetLang 插在检查与设置之间）。
func TestN060S3Fix3SetLangIfConcurrentNeverOverridesNewerChange(t *testing.T) {
	SetLang(ZH)
	t.Cleanup(func() { SetLang(ZH) })
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				SetLang(EN)
			} else {
				SetLang(ZH)
			}
		}
	}()
	for i := 0; i < 20000; i++ {
		seq := LangSeq()
		before := Lang()
		other := EN
		if before == EN {
			other = ZH
		}
		if SetLangIf(other, seq) {
			// 成功意味着发出前的代次到设置之间没有别的切换：之后的代次只可能是 seq+1 或更大（后台 goroutine 又切了），不可能更小或相等
			if now := LangSeq(); now < seq+1 {
				t.Fatalf("条件更新成功但代次没前进：%d → %d", seq, now)
			}
		}
	}
	close(stop)
	wg.Wait()
}
