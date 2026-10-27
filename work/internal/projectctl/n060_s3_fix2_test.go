package projectctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 段 3 返工 2，M4：YAML 解码失败时，类型错误里带着 yaml 库已经解码过的双引号字符串（转义变成了真的标记字符），
// 这条外部错误进了 graph.parse 的参数。统一出口（i18n.wireArg / (*Error).Error()）中和它，英文显示不能出现残缺的打码文本。

// yamlEscaped 是 YAML 双引号字符串里的标记转义（运行时拼出，免得源码里的转义被提前解释）。
func yamlEscaped() string {
	bs := `\`
	return `"` + bs + `uE000` + bs + `uE001` + bs + `uE002` + bs + `uE003` + bs + `uE004"`
}

func TestN060S3Fix2YAMLDecodedError(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	for name, field := range map[string]string{
		"graph_revision": "graph_revision",
		"budget":         "policies:\n  project_budget:\n    max_cost_cny",
		"review_policy":  "nodes:\n  - id: n1\n    review_policy:\n      required",
	} {
		t.Run(name, func(t *testing.T) {
			for _, lang := range []string{i18n.EN, i18n.ZH} {
				i18n.SetLang(lang)
				dir := t.TempDir()
				src := field + ": " + yamlEscaped() + "\n"
				if err := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte(src), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err := LoadGraph(dir)
				if err == nil {
					t.Fatal("应报类型错误")
				}
				code, args, ok := i18n.Wire(err)
				if !ok {
					t.Fatal("应带码")
				}
				shown := deckclient.Text(code, args, err.Error())
				if i18n.HasMark(fmt.Sprint(args)) || i18n.HasMark(err.Error()) || i18n.HasMark(shown) {
					t.Errorf("%s：解码后的伪造标记漏进了错误：args=%#v error=%q shown=%q", lang, args, err.Error(), shown)
				}
				if strings.Contains(shown, "[redacted]") || strings.Contains(shown, "[已打码]") || strings.Contains(err.Error(), "[已打码]") {
					t.Errorf("%s：伪造了打码短语：%q / %q", lang, shown, err.Error())
				}
			}
		})
	}
}
