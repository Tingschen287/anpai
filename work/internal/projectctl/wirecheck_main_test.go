package projectctl

import (
	"os"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

// TestMain 打开 n060 的运行时保险（见 internal/wirecheck）：本包全部测试产生的响应、通知、带码错误，
// 英文规则下不许有本地中文。每次测试运行都开，不靠环境变量，免得被忘掉。
func TestMain(m *testing.M) { os.Exit(wirecheck.Run("projectctl", m)) }
