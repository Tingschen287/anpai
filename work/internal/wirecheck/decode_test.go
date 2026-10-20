package wirecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 段 3（第 2 段复审 4 的必改 1 与建议 1）：入口守卫只证明文件登记过、文件里有一次 Neutralize 调用；
// 转义写法的伪造标记（JSON 的 ""、YAML 的 ""）要解码之后才出现，只在解码前处理字节挡不住。
// 这里补两道结构上的门，行为本身由各包的 TestN060S3Escaped* / TestAuditN060R5Escaped* 逐入口验证：
//
//  1. 「已中和」的文件里，每个解码外部 JSON、YAML 的函数，必须在同一个函数里走解码后的中和
//     （i18n.UnmarshalExternal、i18n.DecodeExternal、i18n.NeutralizeValue、deckd 的 neutralizeValue）。
//     只在解码前对字节做 Neutralize 不算；登记在 decodeAllow 里的函数要写明理由。
//  2. 有解码的「已中和」文件，必须在 escapeTests 里登记至少一个行为测试；这个测试要真在本包目录的测试文件里，
//     文件里要真有转义写法（"ue00"）。登记了但测试没了、或测试里没有转义输入，都失败。

// 解码后的中和调用。
var postDecodeCalls = map[string]bool{
	"UnmarshalExternal": true, "DecodeExternal": true, "NeutralizeValue": true, "neutralizeValue": true,
}

// 原始的解码调用：json.Unmarshal、yaml.Unmarshal、json.NewDecoder（之后调 Decode）。
func isRawDecode(pkg, name string) bool {
	return (pkg == "json" && (name == "Unmarshal" || name == "NewDecoder")) || (pkg == "yaml" && name == "Unmarshal")
}

// decodeAllow：函数自己不做解码后的中和，由调用者做。key 是「文件 · 函数」。
var decodeAllow = map[string]string{
	"internal/deckd/server.go · readBodyRaw": "只解码、不中和：readBody 解码后 neutralizeValue；开子 agent（planSpawn 核对完指纹）与回答决定（option 只比较，比较失败进错误参数前 Store.Answer 先中和）是仅有的两处直接调用，各自写了理由",
}

// escapeTests：有解码的「已中和」文件 → 喂了转义写法输入的行为测试（包目录相对 work/，测试函数名）。
var escapeTests = map[string][]string{
	"internal/agentdeck/client.go":    {"internal/agentdeck · TestAuditN060R5EscapedUpstreamJSON", "internal/agentdeck · TestN060S3EscapedHTTPNestedValues", "internal/agentdeck · TestN060S3EscapedExecJSON"},
	"internal/agentdeck/events.go":    {"internal/agentdeck · TestN060S3EscapedEventStream"},
	"internal/agentdeck/inbox.go":     {"internal/agentdeck · TestN060S3EscapedExecJSON"},
	"internal/agentdeck/launch.go":    {"internal/agentdeck · TestN060S3EscapedExecJSON"},
	"internal/agentdeck/list.go":      {"internal/agentdeck · TestN060S3EscapedExecJSON"},
	"internal/agentdeck/send.go":      {"internal/agentdeck · TestN060S3EscapedExecJSON"},
	"internal/projectctl/client.go":   {"internal/projectctl · TestAuditN060R5EscapedProjectctlJSON", "internal/projectctl · TestN060S3EscapedStartReceipt"},
	"internal/projectctl/commands.go": {"internal/projectctl · TestN060S3EscapedCommandResults"},
	"internal/projectctl/graph.go":    {"internal/projectctl · TestAuditN060R5EscapedGraphYAML", "internal/projectctl · TestN060S3EscapedGraphYAMLEveryShape"},
	"internal/projectctl/snapshot.go": {"internal/projectctl · TestN060S3EscapedSnapshotCache"},
	"internal/dashquota/dashquota.go": {"internal/dashquota · TestN060S3EscapedDashboardResponse", "internal/dashquota · TestN060S3EscapedUsageCacheFile"},
	"internal/deckd/server.go":        {"internal/deckd · TestN060S3EscapedRequestBodiesNeverReachTheStateDir"},
}

type decodeSite struct{ file, fn, call string }

// 解码外部数据的包：适配层三个包（上游 agent-deck、projectctl、Token 看板）的全部生产文件，加 deckd 的 server.go（请求体）。
// 登记成豁免的文件（读 deck 自己写的、含真标记的文件）除外。
func decodesExternalData(rel string) bool {
	if e, ok := intakeFiles[rel]; ok && e.kind == exempt {
		return false
	}
	for _, d := range []string{"internal/agentdeck/", "internal/projectctl/", "internal/dashquota/"} {
		if strings.HasPrefix(rel, d) {
			return true
		}
	}
	return rel == "internal/deckd/server.go"
}

// decodeSitesIn 找出 root 下所有生产文件里的原始解码调用，以及每个函数有没有解码后的中和调用。
func decodeSitesIn(t *testing.T, root string) (sites []decodeSite, hasPost map[string]bool, decodeFiles map[string]bool) {
	t.Helper()
	hasPost, decodeFiles = map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/wirecheck/") || strings.HasPrefix(rel, "internal/i18n/") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", p, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := rel + " · " + fd.Name.Name
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if id, ok := fun.X.(*ast.Ident); ok && isRawDecode(id.Name, fun.Sel.Name) {
						sites = append(sites, decodeSite{rel, fd.Name.Name, id.Name + "." + fun.Sel.Name})
						decodeFiles[rel] = true
					}
					if id, ok := fun.X.(*ast.Ident); ok && id.Name == "i18n" && (fun.Sel.Name == "UnmarshalExternal" || fun.Sel.Name == "DecodeExternal") {
						decodeFiles[rel] = true
					}
					if postDecodeCalls[fun.Sel.Name] {
						hasPost[key] = true
					}
				case *ast.Ident:
					if postDecodeCalls[fun.Name] {
						hasPost[key] = true
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sites, hasPost, decodeFiles
}

func TestN060DecodedExternalValuesAreNeutralizedInTheSameFunction(t *testing.T) {
	root := workRoot(t)
	sites, hasPost, _ := decodeSitesIn(t, root)
	if len(sites) == 0 {
		t.Fatal("一个原始解码调用都没找到，扫描可能坏了（readBodyRaw 里至少有一个）")
	}
	seenAllow := map[string]bool{}
	for _, s := range sites {
		if !decodesExternalData(s.file) {
			continue // 豁免的文件（deck 自己写的状态文件、owner 自己编辑的设置）里的解码不在此列
		}
		key := s.file + " · " + s.fn
		if hasPost[key] {
			continue
		}
		if _, ok := decodeAllow[key]; ok {
			seenAllow[key] = true
			continue
		}
		t.Errorf("%s 的 %s 调用了 %s 解码外部数据，但函数里没有解码后的中和（i18n.UnmarshalExternal / DecodeExternal / NeutralizeValue）：转义写法的伪造标记会漏进来", s.file, s.fn, s.call)
	}
	for k := range decodeAllow {
		if !seenAllow[k] {
			t.Errorf("decodeAllow 里的 %s 已经不是例外（函数没了、或已经做了解码后的中和）：清单不许过期", k)
		}
	}
}

func TestN060EscapeBehaviorTestsAreRegisteredForEveryDecodingIntake(t *testing.T) {
	root := workRoot(t)
	_, _, decodeFiles := decodeSitesIn(t, root)
	var files []string
	for f := range decodeFiles {
		if decodesExternalData(f) {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	if len(files) < 8 {
		t.Fatalf("找到的有解码的已中和文件太少（%v），扫描可能坏了", files)
	}
	for _, f := range files {
		tests := escapeTests[f]
		if len(tests) == 0 {
			t.Errorf("%s 解码外部数据，却没有在 escapeTests 里登记转义写法的行为测试", f)
			continue
		}
		for _, ref := range tests {
			dir, name, _ := strings.Cut(ref, " · ")
			if !testHasEscapedInput(t, filepath.Join(root, dir), name) {
				t.Errorf("%s 登记的测试 %s 不存在，或测试所在的文件里没有转义写法（ue00）的输入", f, ref)
			}
		}
	}
	for f := range escapeTests {
		if !decodeFiles[f] {
			t.Errorf("escapeTests 里的 %s 已经没有解码调用：清单不许过期", f)
		}
	}
}

// testHasEscapedInput：dir 下某个测试文件里有 func name(，而且该文件含有转义写法的文字。
func testHasEscapedInput(t *testing.T, dir, name string) bool {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if strings.Contains(src, "func "+name+"(") {
			return strings.Contains(src, "ue00") || strings.Contains(src, "uE00")
		}
	}
	return false
}
