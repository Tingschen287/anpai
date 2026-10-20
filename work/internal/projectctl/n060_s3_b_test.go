package projectctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

// 段 3 必改 1（第 2 段复审 4）：转义写法的伪造标记。JSON 的 "\ue000" 与 YAML 双引号字符串的 "\ue000" 要解码之后才变成标记字符。

const escapedJSON = `\ue000\ue001\ue002\ue003\ue004`

func fakeProjectctl(t *testing.T, script string) *Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-projectctl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewClient(dir, "unused")
	c.Python = bin
	return c
}

// allWireStrings 取出错误上线路的全部字符串参数（含嵌套码、列表里的）。
func allWireStrings(err error) []string {
	_, args, _ := i18n.Wire(err)
	var out []string
	var walk func(any)
	walk = func(a any) {
		switch v := a.(type) {
		case string:
			out = append(out, v)
		case i18n.Nest:
			for _, x := range v.Args {
				walk(x)
			}
		case i18n.List:
			for _, x := range v.Items {
				walk(x)
			}
		}
	}
	for _, a := range args {
		walk(a)
	}
	return out
}

// Codex 复审 4 的反例，名字沿用。
func TestAuditN060R5EscapedProjectctlJSON(t *testing.T) {
	c := fakeProjectctl(t, "cat <<'JSON'\n"+`{"ok":false,"error":"external `+escapedJSON+` text"}`+"\nJSON\n")
	_, err := c.run(context.Background(), "validate")
	if err == nil {
		t.Fatal("expected error")
	}
	code, args, _ := i18n.Wire(err)
	i18n.SetLang(i18n.EN)
	defer i18n.SetLang(i18n.ZH)
	shown := deckclient.Text(code, args, err.Error())
	if strings.Contains(shown, "[redacted]") {
		t.Errorf("伪造的转义标记被当成真打码显示：%q", shown)
	}
	for _, s := range allWireStrings(err) {
		if i18n.HasMark(s) {
			t.Errorf("错误参数里还有标记：%q", s)
		}
	}
}

func TestAuditN060R5EscapedGraphYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte("nodes:\n  - id: n1\n    title: \"external \\uE000\\uE001\\uE002\\uE003\\uE004 text\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 {
		t.Fatal(g)
	}
	if i18n.HasMark(g.Nodes[0].Title) {
		t.Errorf("YAML 转义绕过了中和：%q", g.Nodes[0].Title)
	}
	if hits := wirecheck.MarksIn(g); len(hits) > 0 {
		t.Errorf("整个 Graph 里还有标记：%v", hits)
	}
}

// YAML 的转义有几种写法：\uXXXX、\UXXXXXXXX，键、列表项、嵌套对象里都要中和。
func TestN060S3EscapedGraphYAMLEveryShape(t *testing.T) {
	dir := t.TempDir()
	yml := "project_id: \"p\\uE000x\"\n" +
		"nodes:\n" +
		"  - id: \"n\\U0000E001\"\n" +
		"    title: \"a\\uE002b\"\n" +
		"    depends_on: [\"d\\uE003\", \"e\\uE004\"]\n" +
		"    review_policy:\n" +
		"      reviewer_role: \"r\\uE000\"\n"
	if err := os.WriteFile(filepath.Join(dir, "work-graph.yaml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g.ProjectID == "" || len(g.Nodes) != 1 || len(g.Nodes[0].DependsOn) != 2 {
		t.Fatalf("没解析出内容，测试没有测到东西：%+v", g)
	}
	if hits := wirecheck.MarksIn(g); len(hits) > 0 {
		t.Errorf("YAML 里这些转义写法的标记还活着：%v", hits)
	}
}

// 子命令的结果：validate、next、verify-chain 的 result 先装进 RawMessage 再解一次，两次解码之后都不能有标记。
func TestN060S3EscapedCommandResults(t *testing.T) {
	c := fakeProjectctl(t, `case "$4" in
validate) cat <<'JSON'
{"ok":true,"result":{"valid":true,"project_id":"p `+escapedJSON+`","mode":"m`+escapedJSON+`"}}
JSON
;;
next) cat <<'JSON'
{"ok":true,"result":[{"id":"n`+escapedJSON+`","title":"t `+escapedJSON+`","depends_on":["d`+escapedJSON+`"]}]}
JSON
;;
verify-chain) cat <<'JSON'
{"ok":true,"result":{"chain_ok":false,"events":3,"error_count":1,"errors":["e `+escapedJSON+`"],"warnings":["w`+escapedJSON+`"]}}
JSON
exit 2
;;
esac
`)
	ctx := context.Background()
	v, err := c.Validate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hits := wirecheck.MarksIn(v); len(hits) > 0 {
		t.Errorf("validate 结果里还有标记：%v", hits)
	}
	n, err := c.Next(ctx)
	if err != nil || len(n) != 1 {
		t.Fatalf("%v %v", n, err)
	}
	if hits := wirecheck.MarksIn(n); len(hits) > 0 {
		t.Errorf("next 结果里还有标记：%v", hits)
	}
	ch, err := c.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hits := wirecheck.MarksIn(ch); len(hits) > 0 {
		t.Errorf("verify-chain 结果里还有标记：%v", hits)
	}
}

// 开工：回执里的节点号带转义标记，对不上，错误参数里不能有标记。
func TestN060S3EscapedStartReceipt(t *testing.T) {
	c := fakeProjectctl(t, `case "$4" in
verify-chain) cat <<'JSON'
{"ok":true,"result":{"chain_ok":true,"events":3}}
JSON
;;
start) cat <<'JSON'
{"ok":true,"result":{"node_id":"n9`+escapedJSON+`","attempt":1}}
JSON
;;
esac
`)
	_, err := c.Start(context.Background(), StartRequest{NodeID: "n1", Actor: "a", Role: "r", RouteClass: "c", Model: "m", Reason: "x"})
	if err == nil {
		t.Fatal("回执对不上，应该失败")
	}
	ss := allWireStrings(err)
	if len(ss) == 0 {
		t.Fatalf("错误参数里没有字符串，测试没有测到东西：%v", err)
	}
	for _, s := range ss {
		if i18n.HasMark(s) {
			t.Errorf("开工回执的错误参数里还有标记：%q", s)
		}
	}
}

// 快照缓存文件（落盘的终态表）里的节点号带转义标记。
func TestN060S3EscapedSnapshotCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache.json")
	doc := `{"project":"` + dir + `","events":3,"states":{"n1` + escapedJSON + `":"accepted","n2":"accepted"}}`
	if err := os.WriteFile(cache, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReader(dir, "unused").WithCache(cache)
	r.loadCache(3)
	if len(r.final) != 2 {
		t.Fatalf("缓存没读进来，测试没有测到东西：%v", r.final)
	}
	if hits := wirecheck.MarksIn(r.final); len(hits) > 0 {
		t.Errorf("缓存里的节点号还有标记：%v", hits)
	}
}
