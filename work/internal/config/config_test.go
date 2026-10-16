package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const good = `{
  "upstream_binary": "/usr/local/bin/agent-deck",
  "profile": "_workdeck",
  "upstream_addr": "127.0.0.1:8420",
  "group": "graph",
  "models": [
    {"id": "claude-opus-5", "tool": "claude", "route_class": "frontier"},
    {"id": "claude-sonnet-5", "tool": "claude", "route_class": "balanced"}
  ]
}`

func TestLoadGoodConfig(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil {
		t.Fatalf("合法配置不该报错：%v", err)
	}
	if c.Profile != "_workdeck" || len(c.Models) != 2 {
		t.Errorf("配置没读全：%+v", c)
	}
	tbl, err := c.RouteTable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tbl.Lookup("claude-opus-5"); err != nil {
		t.Errorf("档位表里应当有这个模型：%v", err)
	}
}

// profile 留空会落到上游默认 profile，可能覆盖真实会话，必须拦住。
func TestEmptyProfileIsRefused(t *testing.T) {
	body := strings.Replace(good, `"profile": "_workdeck"`, `"profile": ""`, 1)
	_, err := Load(write(t, body))
	if err == nil {
		t.Fatal("profile 留空必须报错")
	}
	if !strings.Contains(err.Error(), "覆盖真实会话") {
		t.Errorf("报错没说清后果：%v", err)
	}
}

// 没有档位表就没法确认模型档位，派工一律会被拒，配置阶段就该说清楚。
func TestEmptyModelTableIsRefused(t *testing.T) {
	body := strings.Replace(good, `"models": [
    {"id": "claude-opus-5", "tool": "claude", "route_class": "frontier"},
    {"id": "claude-sonnet-5", "tool": "claude", "route_class": "balanced"}
  ]`, `"models": []`, 1)
	if _, err := Load(write(t, body)); err == nil {
		t.Fatal("空的档位表必须报错")
	}
}

func TestIncompleteModelRowIsRefused(t *testing.T) {
	body := strings.Replace(good,
		`{"id": "claude-sonnet-5", "tool": "claude", "route_class": "balanced"}`,
		`{"id": "claude-sonnet-5", "tool": "claude"}`, 1)
	if _, err := Load(write(t, body)); err == nil {
		t.Fatal("模型行没写全必须报错")
	}
}

// 配置文件不存在时要说清楚缺什么，不能悄悄用一套默认值去派工。
func TestMissingConfigExplainsWhatIsNeeded(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "没有这个文件.json"))
	if err == nil {
		t.Fatal("配置不存在必须报错")
	}
	for _, want := range []string{"上游路径", "profile", "模型档位表"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错没提到需要 %q：%v", want, err)
		}
	}
}

// token 只走环境变量，配置结构里根本没有这个字段。
func TestTokenNeverComesFromTheConfigFile(t *testing.T) {
	body := strings.Replace(good, `"group": "graph"`, `"group": "graph", "token": "不该被读到的值"`, 1)
	c, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	// 结构体里没有 token 字段，多余的键会被忽略。
	data, err := os.ReadFile(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "不该被读到的值") {
		t.Fatal("测试前提不成立")
	}
	if strings.Contains(c.UpstreamBinary+c.Profile+c.UpstreamAddr+c.Group, "不该被读到的值") {
		t.Error("配置文件里的 token 被读进了某个字段")
	}

	t.Setenv(TokenEnv, "来自环境变量")
	if Token() != "来自环境变量" {
		t.Error("token 应当从环境变量读")
	}
	t.Setenv(TokenEnv, "")
	if Token() != "" {
		t.Error("环境变量没设时 token 应当为空")
	}
}

// 能力位声明与节点能力要求都要校验：认不出的名字、yes/no 以外的值都报错（n039）。
func TestCapabilityConfigIsValidated(t *testing.T) {
	base := func() Config {
		return Config{UpstreamBinary: "/x", Profile: "_p", Models: []ModelEntry{{ID: "m", Tool: "t", Class: "frontier"}}}
	}
	c := base()
	c.Models[0].Capabilities = map[string]string{"resume": "yes"}
	c.NodeCapabilities = map[string][]string{"n1": {"resume"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("合法配置被拒：%v", err)
	}
	c = base()
	c.Models[0].Capabilities = map[string]string{"resume": "maybe"}
	if err := c.Validate(); err == nil {
		t.Error("能力值只能是 yes/no")
	}
	c = base()
	c.NodeCapabilities = map[string][]string{"n1": {"telepathy"}}
	if err := c.Validate(); err == nil {
		t.Error("不认识的能力要求应当报错")
	}
	c = base()
	c.Models[0].Capabilities = map[string]string{"mcp": "no"}
	table, err := c.RouteTable()
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := table.Lookup("m"); m.Caps["mcp"] != "no" {
		t.Error("能力声明要带进档位表")
	}
}

// n042：shell 没写 model_not_applicable、或有模型的工具写了它，配置加载就报错并说清楚。
func TestModelNotApplicableIsCheckedAgainstTheTool(t *testing.T) {
	base := func(entry string) string {
		return `{"upstream_binary":"/bin/true","profile":"_t","models":[` + entry + `]}`
	}
	for name, c := range map[string]struct {
		entry string
		ok    bool
	}{
		"shell 声明了":  {`{"id":"sh","tool":"shell","route_class":"economy","model_not_applicable":true}`, true},
		"shell 没声明":  {`{"id":"sh","tool":"shell","route_class":"economy"}`, false},
		"claude 声明了": {`{"id":"c","tool":"claude","route_class":"frontier","model_not_applicable":true}`, false},
		"claude 正常":  {`{"id":"c","tool":"claude","route_class":"frontier"}`, true},
	} {
		p := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(p, []byte(base(c.entry)), 0o600)
		cfg, err := Load(p)
		if (err == nil) != c.ok {
			t.Errorf("%s：期望 ok=%v，实际 %v", name, c.ok, err)
		}
		if c.ok && c.entry[len(c.entry)-6:] == "true}" {
			tbl, _ := cfg.RouteTable()
			if m, _ := tbl.Lookup("sh"); !m.NoModel {
				t.Errorf("%s：NoModel 没传进档位表", name)
			}
		}
	}
}

// n044：自定义工具显式声明 agent 才派；声明要传进档位表；shell 不能声明。
func TestAgentDeclarationIsLoadedAndShellCannotClaimIt(t *testing.T) {
	base := func(entry string) string {
		return `{"upstream_binary":"/bin/true","profile":"_t","models":[` + entry + `]}`
	}
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(base(`{"id":"grok-4","tool":"grok","route_class":"frontier","agent":true}`)), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("声明了 agent 的自定义工具应当能加载：%v", err)
	}
	tbl, _ := cfg.RouteTable()
	if m, _ := tbl.Lookup("grok-4"); !m.DeclaredAgent {
		t.Error("agent 声明没传进档位表")
	}
	os.WriteFile(p, []byte(base(`{"id":"sh","tool":"shell","route_class":"economy","model_not_applicable":true,"agent":true}`)), 0o600)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "不能声明") {
		t.Errorf("shell 声明 agent 应当报错：%v", err)
	}
}
