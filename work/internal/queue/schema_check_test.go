package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 一个只够用的 JSON Schema 校验器：只实现 schema.json 里真的出现的关键字。
//
// 不引第三方库，是为了不给仓库添依赖；代价是它不是完整实现。
// schema.json 用到别的关键字时，check 会直接报错，而不是静默放过——见 knownKeywords。
var knownKeywords = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true, "$defs": true,
	"type": true, "required": true, "additionalProperties": true, "properties": true,
	"items": true, "enum": true, "const": true, "minLength": true, "minItems": true,
	"pattern": true, "$ref": true, "oneOf": true, "format": true,
}

type schemaChecker struct {
	root map[string]any
}

func loadSchema(t *testing.T) *schemaChecker {
	t.Helper()
	data, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("schema.json 不是合法 JSON：%v", err)
	}
	return &schemaChecker{root: root}
}

func (c *schemaChecker) validate(v any) []string {
	return c.check(c.root, v, "$")
}

func (c *schemaChecker) check(s map[string]any, v any, path string) []string {
	var errs []string
	for k := range s {
		if !knownKeywords[k] {
			return []string{fmt.Sprintf("%s：schema 用了本校验器不认识的关键字 %q", path, k)}
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		def, ok := c.root["$defs"].(map[string]any)[name].(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s：找不到 %s", path, ref)}
		}
		return c.check(def, v, path)
	}
	if alts, ok := s["oneOf"].([]any); ok {
		matched := 0
		for _, a := range alts {
			if len(c.check(a.(map[string]any), v, path)) == 0 {
				matched++
			}
		}
		if matched != 1 {
			errs = append(errs, fmt.Sprintf("%s：oneOf 命中 %d 个分支，应当恰好 1 个", path, matched))
		}
		return errs
	}
	if t, ok := s["type"]; ok && !typeMatches(t, v) {
		return []string{fmt.Sprintf("%s：类型不对，要 %v，实际是 %T", path, t, v)}
	}
	if want, ok := s["const"]; ok && fmt.Sprint(want) != fmt.Sprint(v) {
		errs = append(errs, fmt.Sprintf("%s：应当是 %v，实际是 %v", path, want, v))
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s：%v 不在枚举 %v 里", path, v, enum))
		}
	}
	if str, ok := v.(string); ok {
		if n, ok := s["minLength"].(float64); ok && len([]rune(str)) < int(n) {
			errs = append(errs, fmt.Sprintf("%s：字符串太短", path))
		}
		if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(str) {
			errs = append(errs, fmt.Sprintf("%s：%q 不符合 %s", path, str, p))
		}
		if f, _ := s["format"].(string); f == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
				errs = append(errs, fmt.Sprintf("%s：%q 不是 date-time", path, str))
			}
		}
	}
	if obj, ok := v.(map[string]any); ok {
		for _, r := range asStrings(s["required"]) {
			if _, ok := obj[r]; !ok {
				errs = append(errs, fmt.Sprintf("%s：缺少必需字段 %q", path, r))
			}
		}
		props, _ := s["properties"].(map[string]any)
		for k, fv := range obj {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if ap, ok := s["additionalProperties"].(bool); ok && !ap {
					errs = append(errs, fmt.Sprintf("%s：多出不允许的字段 %q", path, k))
				}
				continue
			}
			errs = append(errs, c.check(ps, fv, path+"."+k)...)
		}
	}
	if arr, ok := v.([]any); ok {
		if n, ok := s["minItems"].(float64); ok && len(arr) < int(n) {
			errs = append(errs, fmt.Sprintf("%s：至少要 %d 项", path, int(n)))
		}
		if is, ok := s["items"].(map[string]any); ok {
			for i, e := range arr {
				errs = append(errs, c.check(is, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func typeMatches(t any, v any) bool {
	var types []string
	switch tt := t.(type) {
	case string:
		types = []string{tt}
	case []any:
		types = asStrings(tt)
	}
	for _, name := range types {
		switch name {
		case "null":
			if v == nil {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "integer":
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				return true
			}
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		}
	}
	return false
}

func asStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, a := range arr {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// 校验器自己得先过阳性对照：明显坏的文件必须报错，否则它的「通过」没有意义。
func TestSchemaCheckerRejectsBrokenFiles(t *testing.T) {
	c := loadSchema(t)
	good := map[string]any{"version": float64(1), "items": []any{}}
	if errs := c.validate(good); len(errs) != 0 {
		t.Fatalf("空队列应当通过：%v", errs)
	}
	var sample map[string]any
	data, _ := json.Marshal(file{Version: 1, Items: []Item{sampleItem()}})
	_ = json.Unmarshal(data, &sample)
	if errs := c.validate(sample); len(errs) != 0 {
		t.Fatalf("样例条目应当通过：%v", errs)
	}
	breakers := map[string]func(it map[string]any){
		"缺 recommendation":    func(it map[string]any) { delete(it, "recommendation") },
		"category 不在枚举":       func(it map[string]any) { it["category"] = "whatever" },
		"confidence 写成小数":     func(it map[string]any) { it["recommendation"].(map[string]any)["confidence"] = 0.8 },
		"deadline 不是时间":       func(it map[string]any) { it["deadline"] = "明天" },
		"options 为空":          func(it map[string]any) { it["options"] = []any{} },
		"多出字段":                func(it map[string]any) { it["extra"] = true },
		"blocking_sessions 缺": func(it map[string]any) { delete(it, "blocking_sessions") },
		"id 格式不对":             func(it map[string]any) { it["id"] = "x" },
	}
	for name, breakIt := range breakers {
		var doc map[string]any
		_ = json.Unmarshal(data, &doc)
		breakIt(doc["items"].([]any)[0].(map[string]any))
		if errs := c.validate(doc); len(errs) == 0 {
			t.Errorf("%s：校验器应当报错，却通过了", name)
		}
	}
}

func sampleItem() Item {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	return Item{
		ID: "session:0123456789abcdef01234567", Category: CatSessionWaiting, Risk: RiskMedium,
		Title: "会话在等你输入：x", Detail: "running → waiting",
		Options:          []Option{{Key: "continue", Label: "继续", Message: "继续"}},
		Recommendation:   Recommendation{Option: "continue", Confidence: ConfidenceLow, Basis: "规则"},
		EvidenceRefs:     []string{"agent-deck session:s1"},
		BlockingSessions: []string{"s1"},
		Source:           Source{Kind: SourceSession, SessionID: "s1", Present: true},
		FirstSeen:        now, LastSeen: now, Status: StatusOpen,
	}
}
