package agentdeck

import "testing"

// n067 返工 3（owner 选定）：连串规则两个平台都只查最后一个 argv 元素。
//
// 连串只会出现在 argv 的最后一个元素：参数区末尾的 NUL 被改写后，最后一个参数连上紧跟其后的环境变量。
// 所以非 CLI 进程的连串识别（--flagKEY=…、--flag=badKEY=…、「参数名 + badKEY=…」）只对最后一个元素做；
// 原有逐字相等的检查（checkBypassFlags，含 argv 连成一串的那份）不变，对每个位置都查。
// 保守的代价（两平台相同，owner 接受）：同样的字面串放在最后仍会被拒，哪怕进程没改写过内存。
//
// 本文件只经 procArgsOK + 假的 /proc 目录（argallow_glue_test.go 的 fakeProcWith）测，c051f67 上原样编译、先失败。

func gluedVerdict(t *testing.T, argv ...string) error {
	t.Helper()
	_, err := procArgsOK(fakeProcWith(t, argv...), 4242, LaunchArgs{Tool: "claude", Binary: "claude"})
	return err
}

// Codex 的 native-false-positive 例子：合法的字面参数（另一个程序自己的长参数、printf 要打印的文字、文档 helper）在非末尾位置放行。
func TestGlueRuleLetsLiteralArgumentsThroughWhenNotLast(t *testing.T) {
	for _, argv := range [][]string{
		{"/usr/bin/printf", "%s\n", "--yoloMODEL=demo", "tail"},
		{"helper", "--dangerously-skip-permissionsDocumentation=enabled", "tail"},
		{"tool", "--permission-mode=bypassPermissionsDoc=example", "tail"},
		{"tool", "--allow-dangerously-skip-permissionsHOME=/x", "-v"},
		{"tool", "--permission-mode", "bypassPermissionsPATH=x", "tail"}, // 参数名和取值都不在末尾
		{"bash", "-c", "echo --yoloMODEL=demo", "x"},                     // 词在中间的元素里
		{"tool", "--yoloMODEL=demo", "--yolo2=x"},                        // 末尾那个本来就不是连串
	} {
		if err := gluedVerdict(t, argv...); err != nil {
			t.Errorf("非末尾的字面串应放行：%q：%v", argv, err)
		}
	}
}

// 同样的字面串放在最后一个元素：仍拒绝（保守代价，两平台相同）。
func TestGlueRuleStillRefusesTheSameLiteralsWhenLast(t *testing.T) {
	for _, argv := range [][]string{
		{"/usr/bin/printf", "%s\n", "--yoloMODEL=demo"},
		{"helper", "--dangerously-skip-permissionsDocumentation=enabled"},
		{"tool", "--permission-mode=bypassPermissionsDoc=example"},
		{"tool", "--permission-mode", "bypassPermissionsPATH=x"}, // 倒数第二个是参数名，最后一个是 badKEY=…
		{"tool", "--ask-for-approval", "neverLANG=C"},
		{"tool", "--sandbox", "danger-full-accessHOME=/x"},
		{"bash", "-c", "echo hi --yoloMODEL=demo"}, // 最后一个元素里的词
		{"bash", "-c", "x", "--dangerously-skip-permissionsPATH=/usr/bin"},
	} {
		if err := gluedVerdict(t, argv...); err == nil {
			t.Errorf("末尾的连串应拒绝：%q", argv)
		}
	}
}

// 原有的逐字规则不变：每个位置都查，argv 连成一串的那份也查（参数名和取值分在两个元素里、都不在末尾，照旧拒绝）。
func TestExactBypassChecksStillLookAtEveryPosition(t *testing.T) {
	for _, argv := range [][]string{
		{"tool", "--dangerously-skip-permissions", "tail"},
		{"tool", "--yolo", "tail"},
		{"tool", "--permission-mode=bypassPermissions", "tail"},
		{"tool", "--permission-mode", "bypassPermissions", "tail"},
		{"tool", "--sandbox", "danger-full-access", "tail"},
	} {
		if err := gluedVerdict(t, argv...); err == nil {
			t.Errorf("逐字的绕过参数在任何位置都应拒绝：%q", argv)
		}
	}
}

// 取值连串：只认最后一个元素里的 --flag=badKEY=…，以及「倒数第二个是参数名、最后一个是 badKEY=…」。
func TestGlueRuleValueForms(t *testing.T) {
	for _, c := range []struct {
		argv   []string
		refuse bool
	}{
		{[]string{"tool", "--permission-mode=bypassPermissionsHOME=/x"}, true},             // 最后一个元素里的 --flag=badKEY=
		{[]string{"tool", "--permission-mode=bypassPermissionsHOME=/x", "tail"}, false},    // 不在最后
		{[]string{"tool", "--permission-mode", "bypassPermissionsHOME=/x"}, true},          // 参数名在倒数第二个
		{[]string{"tool", "--permission-mode", "x", "bypassPermissionsHOME=/x"}, false},    // 参数名不在倒数第二个，最后一个不是参数
		{[]string{"tool", "--permission-mode", "bypassPermissionsHOME=/x", "tail"}, false}, // 取值不在最后
		{[]string{"tool", "--permission-mode", "bypassPermissions2=x"}, false},             // 后缀不是 KEY=，沿用原语义
		{[]string{"tool", "--permission-mode", "default"}, false},                          // 普通
		{[]string{"tool", "--sandbox=danger-full-accessPATH=x"}, true},                     // sandbox 同理
		{[]string{"tool", "--sandbox=danger-full-accessPATH=x", "--other"}, false},         // 不在最后
		{[]string{"tool", "--ask-for-approval", "neverLANG=C"}, true},                      // 倒数第二个是参数名
		{[]string{"tool", "--ask-for-approval", "neverLANG=C", "x"}, false},                // 不在倒数第二个
		{[]string{"tool", "--permission-mode", "'bypassPermissionsPATH=x'"}, true},         // 带引号的取值仍认
		{[]string{"tool", "x --permission-mode bypassPermissionsPATH=x"}, true},            // 最后一个元素内部「名 值」两个词
		{[]string{"tool", "--permission-mode bypassPermissionsPATH=x", "tail"}, false},     // 同上，但元素不在最后
		{[]string{"tool", "--permission-mode", "bypassPermissionsPATH=two words"}, true},   // 值里有空格，第一个词已有 KEY=
	} {
		err := gluedVerdict(t, c.argv...)
		if (err != nil) != c.refuse {
			t.Errorf("%q：拒绝=%v，期望 %v（%v）", c.argv, err != nil, c.refuse, err)
		}
	}
}

// CLI 进程不经过连串检查：合法的 claude 命令行照常通过，连串在白名单里因「不认识的参数」被拒（位置无关）。
func TestCLIProcessGlueStillGoesThroughTheWhitelistAnywhere(t *testing.T) {
	want := LaunchArgs{Tool: "claude", Binary: "claude"}
	if ok, err := procArgsOK(fakeProcWith(t, "/usr/bin/claude", "--session-id", "u-1", "--dangerously-skip-permissionsPATH=/x", "--model", "m"), 4242, want); !ok || err == nil {
		t.Errorf("CLI 进程里非末尾的连串也应被白名单拒绝：%v %v", ok, err)
	}
}
