package projectctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// n060 段 3 返工 3，R2-1（路径 3）：项目目录名是外部文字，进带码错误的路径参数是普通 string，
// 要在建立显示用参数时先中和（业务上读文件仍用原路径）。
// 反例来自 review-2/audit_new_projectctl_test.go 的 TestAuditS3R2GraphPathStringForgesMark，这里用真实文件系统输入。

func forgedProjectDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "external-"+i18n.RedactMark)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertNoForgedMark(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：应当出错", what)
	}
	code, args, ok := i18n.Wire(err)
	if !ok {
		t.Fatalf("%s：应是带码错误：%v", what, err)
	}
	for _, lang := range []string{i18n.EN, i18n.ZH} {
		i18n.SetLang(lang)
		shown := deckclient.Text(code, args, err.Error())
		for i, a := range args {
			if s, isStr := a.(string); isStr && i18n.HasMark(s) {
				t.Errorf("%s：第 %d 个参数仍带标记字符：%q", what, i, s)
			}
		}
		if strings.Contains(shown, "[redacted]") || strings.Contains(shown, "[已打码]") || i18n.HasMark(shown) {
			t.Errorf("%s（%s）：外部目录名被当成打码：%q", what, lang, shown)
		}
	}
	i18n.SetLang(i18n.ZH)
	if i18n.HasMark(err.Error()) {
		t.Errorf("%s：err.Error() 里不该有标记字符：%q", what, err.Error())
	}
}

func TestN060S3Fix3GraphPathStringDoesNotForgeMark(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir := forgedProjectDir(t)
	_, err := LoadGraph(dir) // 目录里没有 work-graph.yaml：真实的读文件失败
	assertNoForgedMark(t, "LoadGraph 读不了", err)
	// 解析失败、空图：路径同样
	if werr := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte("nodes: [\n"), 0600); werr != nil {
		t.Fatal(werr)
	}
	_, err = LoadGraph(dir)
	assertNoForgedMark(t, "LoadGraph 解析失败", err)
	if werr := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte("nodes: []\n"), 0600); werr != nil {
		t.Fatal(werr)
	}
	_, err = LoadGraph(dir)
	assertNoForgedMark(t, "LoadGraph 空图", err)
}

// 业务原值不变：读文件仍用原路径（含标记字符的目录能正常读到图）。
func TestN060S3Fix3GraphStillReadsTheRealPath(t *testing.T) {
	dir := forgedProjectDir(t)
	if err := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte("nodes:\n  - id: n1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(dir)
	if err != nil || len(g.Nodes) != 1 {
		t.Fatalf("原路径应能读到：%v %+v", err, g)
	}
}

// 同包的近邻入口：读状态投影的路径参数（同样是项目目录名）。
func TestN060S3Fix3ProjectionPathStringDoesNotForgeMark(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir := forgedProjectDir(t)
	_, err := LoadProjection(dir) // 没有 PROJECT-STATUS.md
	assertNoForgedMark(t, "LoadProjection 读不了", err)
}

// 读状态投影读到一半失败（一行超过扫描上限）：同一个路径参数。
func TestN060S3Fix3ProjectionScanErrorPathDoesNotForgeMark(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir := forgedProjectDir(t)
	long := strings.Repeat("x", 9<<20) // 超过 LoadProjection 给扫描器设的单行上限（8MB）
	if err := os.WriteFile(filepath.Join(dir, "PROJECT-STATUS.md"), []byte(long+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProjection(dir)
	assertNoForgedMark(t, "LoadProjection 读行失败", err)
}
