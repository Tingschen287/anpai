#!/usr/bin/env python3
"""阳性对照：每一处都故意把清单或代码改坏，对应的测试必须变红。

对照清单直接对着独立评审给 attempt 12 的四条 blocker 写。
绿的测试不证明任何事——要证明它在盯着，就得让它红一次。
"""
import subprocess, pathlib, sys, os

# 脚本住在 work/scripts/ 下，work 就是上一级。
WORK = pathlib.Path(__file__).resolve().parent.parent
ENV = dict(os.environ)
if os.path.isdir(os.path.expanduser("~/.local/go/bin")):
    ENV["PATH"] = os.path.expanduser("~/.local/go/bin") + ":" + ENV["PATH"]
if not ENV.get("WORKDECK_UPSTREAM_SRC"):
    print("需要先设 WORKDECK_UPSTREAM_SRC 指向上游源码快照", file=sys.stderr)
    raise SystemExit(2)

def run_test(pattern, pkg="./internal/agentdeck/"):
    r = subprocess.run(["go", "test", pkg, "-run", pattern, "-count=1"],
                       cwd=WORK, env=ENV, capture_output=True, text=True)
    return r.returncode != 0, (r.stdout + r.stderr)

CASES = []
def case(desc, blocker, path, old, new, pattern, pkg="./internal/agentdeck/"):
    """old 为 None 表示新建 path 这个文件，跑完删掉。"""
    CASES.append((desc, blocker, path, old, new, pattern, pkg))

M = "internal/agentdeck/manifest.go"
T = "internal/agentdeck/client_test.go"
PAT = "TestSideEffectEvidencePointsAtRealUpstreamCode"
GUARD = "TestAdapterMayAttachButNeverSendsKeysOrKills"

# —— blocker 1：断言可以是假话（attempt 12 的 Does 抄锚点即通行证）——
case("Effect 说调用了一个上游没有的函数", "B1", M,
     'Effect: Effect{Kind: EffectCalls, Target: "inst.PreviewFull"},',
     'Effect: Effect{Kind: EffectCalls, Target: "inst.DeleteEverything"},', PAT)

case("EffectCalls 的常量参数写成假的", "B1", M,
     'Effect: Effect{Kind: EffectCalls, Target: "s.runBoundedOutput", Arg: "capture-pane"},',
     'Effect: Effect{Kind: EffectCalls, Target: "s.runBoundedOutput", Arg: "send-keys-now"},', PAT)

case("EffectDeclaresConst 的值写成假的", "B1", M,
     'Effect: Effect{Kind: EffectDeclaresConst, Target: "toolDataLastActivityAtKey", Arg: "last_activity_at"},',
     'Effect: Effect{Kind: EffectDeclaresConst, Target: "toolDataLastActivityAtKey", Arg: "totally_made_up"},', PAT)

case("EffectDeclares 说上游有个不存在的函数", "B1", M,
     'Effect: Effect{Kind: EffectDeclares, Target: "persistLastActivity"},',
     'Effect: Effect{Kind: EffectDeclares, Target: "persistNothingAtAll"},', PAT)

# —— blocker 3：Scope 只验非空，填废话也过 ——
case("NotCovered 漏掉一个真实调用", "B3", M,
     '"string", "time.Now", "tmux.GetTerminalInfo",',
     '"string", "time.Now",', PAT)

case("NotCovered 里编一个不存在的调用", "B3", M,
     '"clipboard.Copy", "flag.NewFlagSet", "fmt.Errorf",',
     '"clipboard.Copy", "flag.NewFlagSet", "fmt.Errorf", "pkg.TotallyFakeCall",', PAT)

# —— 落点核对 ——
case("Evidence 的所属函数写错", "落点", M,
     'Anchor: "inst.PreviewFull()",\n\t\t\t\t\tWithin: "handleSessionOutput",',
     'Anchor: "inst.PreviewFull()",\n\t\t\t\t\tWithin: "handleSessionSend",', PAT)

case("Evidence 的所属函数留空想蒙混", "落点", M,
     'Anchor: "storage.InsertSessionAndVerify(newInstance, tree)",\n\t\t\t\t\tWithin: "handleLaunch",',
     'Anchor: "storage.InsertSessionAndVerify(newInstance, tree)",\n\t\t\t\t\tWithin: "",', PAT)

# —— blocker 4：守边可被拼接与改名绕过 ——
case("禁词写成字符串拼接", "B4", M,
     'var keyInjectionCommands = []string{"send-keys", "paste-buffer"}',
     'var keyInjectionCommands = []string{"send-keys", "paste-buffer"}\n\nvar sneakyProbe = "send" + "-keys"', GUARD)

case("另起一个名字含禁令变量名的常量", "B4", M,
     'var keyInjectionCommands = []string{"send-keys", "paste-buffer"}',
     'var keyInjectionCommands = []string{"send-keys", "paste-buffer"}\n\nvar alternatekeyInjectionCommands = []string{"send-keys"}', GUARD)

# —— 自查时发现的两处，补上对照 ——
case("一条 var 同时声明禁令变量和另一个含禁词的变量", "自查", M,
     'var keyInjectionCommands = []string{"send-keys", "paste-buffer"}',
     'var keyInjectionCommands, sneaky = []string{"send-keys", "paste-buffer"}, "send-keys"', GUARD)

case("一条 Claim 挂两条落点，第二条没人查", "自查", M,
     '''Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   4409,
					Anchor: "inst.PreviewFull()",
					Within: "handleSessionOutput",
				}},''',
     '''Evidence: []Evidence{{
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   4409,
					Anchor: "inst.PreviewFull()",
					Within: "handleSessionOutput",
				}, {
					Path:   "cmd/agent-deck/session_cmd.go",
					Line:   2894,
					Anchor: "executeSend(tmuxSess, inst.Tool, message",
					Within: "handleSessionSend",
				}},''', PAT)

# —— attempt 14：对着第十四轮评审的三条 blocker ——
case("Effect 换成同函数里另一个真实但无关的调用", "A14-1", M,
     'Effect: Effect{Kind: EffectCalls, Target: "tmuxSession.Attach"},',
     'Effect: Effect{Kind: EffectCalls, Target: "os.Exit"},', PAT)

case("Effect 只写尾名，不写完整调用形式", "A14-1", M,
     'Effect: Effect{Kind: EffectCalls, Target: "inst.PreviewFull"},',
     'Effect: Effect{Kind: EffectCalls, Target: "PreviewFull"},', PAT)

case("In 里那一跳不声明调用写法（Call 留空）", "A14-2", M,
     '''							Call: "inst.PreviewFull",\n''', '', PAT)

case("命令参数塞一个算出来的字符串", "A14-3", "internal/agentdeck/attach.go",
     'args = append(args, "attach-session", "-t", exactTarget(tmuxSession))',
     'args = append(args, "attach-session", "-t", exactTarget(tmuxSession))\n\targs = append(args, string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115}))',
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

# —— attempt 16：对着第十六轮评审的三条 blocker ——
case("Anchor 落在依据行上，却不在被选中那个调用的节点里", "A16-1", M,
     'Anchor: "tmuxSession.Attach(ctx, detachByte)",',
     'Anchor: "if err := tmuxSession",', PAT)

case("In 声称的调用写法在上游不存在", "A16-2", M,
     'Call: "inst.PreviewFull",',
     'Call: "other.PreviewFull",', PAT)

case("In 带了调用写法却不交代变量类型（Note 空）", "A16-2", M,
     '''Call: "i.tmuxSession.CaptureFullHistory",
							Note: "Instance.PreviewFull 里的 i.tmuxSession 是 *tmux.Session 字段；" +
								"这一跳是人读出来的",''',
     'Call: "i.tmuxSession.CaptureFullHistory",', PAT)

case("命令参数用间接赋值塞进算出来的字符串", "A16-3", "internal/agentdeck/socketscan.go",
     '\targs = append(args, "show-environment", "-t", exactTarget(tmuxSession), "AGENTDECK_INSTANCE_ID")',
     '\tevil := string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})\n\targs = append(args, evil, "show-environment", "-t", exactTarget(tmuxSession), "AGENTDECK_INSTANCE_ID")',
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

# —— attempt 16 自查：提交后自己又看出两个洞 ——
case("In 的链顺序被打乱（拿掉入口函数）", "A16-自查1", M,
     '\t\t\t\t\t\t{Path: "cmd/agent-deck/session_cmd.go", Func: "handleSessionOutput"},\n',
     '', PAT)

case("命令参数引用一个折叠不出值的包级变量", "A16-自查2", "internal/agentdeck/socketscan.go",
     '''func instanceIDOf(ctx context.Context, socket, tmuxSession string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{}''',
     '''var opaqueProbe = string([]byte{115, 101, 110, 100})

func instanceIDOf(ctx context.Context, socket, tmuxSession string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := []string{opaqueProbe}''',
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

# —— attempt 17：对着第十七轮评审的跨函数反例 ——
case("把命令构造包一层，实参从另一个函数拿", "A17-1", "internal/agentdeck/attach.go",
     """	return cmd, note, err
}""",
     """	return cmd, note, err
}

func opaqueAcrossFunction() string {
	return string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})
}

func opaqueBuilder(command string) {
	_ = exec.Command(command)
}

func callsOpaqueBuilder() {
	opaqueBuilder(opaqueAcrossFunction())
}""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

case("用局部变量中转一个造出来的字符串", "A17-自查", "internal/agentdeck/attach.go",
     """	return cmd, note, err
}""",
     """	return cmd, note, err
}

func opaqueViaLocal() string {
	x := string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})
	return x
}

func opaqueBuilder2(command string) { _ = exec.Command(command) }

func callsIt() { opaqueBuilder2(opaqueViaLocal()) }""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

# —— attempt 18：对着第十八轮评审的两条 ——
case("用 tmux 的命令分隔符 ; 骗过位置状态机", "A18-1", "internal/agentdeck/attach.go",
     """	return cmd, note, err
}""",
     """	return cmd, note, err
}

func opaqueSep() string { return string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115}) }

func bypassViaSeparator() {
	_ = exec.Command("tmux", append([]string{}, "list-sessions", ";", opaqueSep())...)
}""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

case("用本包方法造子命令", "A18-2", "internal/agentdeck/attach.go",
     """	return cmd, note, err
}""",
     """	return cmd, note, err
}

type runnerProbe struct{}

func (runnerProbe) command() string { return string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115}) }

func bypassViaMethod() { _ = exec.Command("tmux", runnerProbe{}.command()) }""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

# —— attempt 20：对着第二十轮评审的三条 ——
SS = "internal/agentdeck/socketscan.go"
TAIL = """	return probes, nil
}"""

case("用类型别名 type S = string 藏住造出来的字符串", "A20-1", SS, TAIL,
     TAIL + """

type S = string

func opaqueAlias() S { return string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115}) }

func bypassAlias() { _ = exec.Command("tmux", opaqueAlias()) }""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

case("把造出来的字符串藏进结构体字段", "A20-2", SS, TAIL,
     TAIL + """

type Payload struct{ Command string }

func buildPayload() Payload {
	return Payload{Command: string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})}
}

func bypassStruct() { p := buildPayload(); _ = exec.Command("tmux", p.Command) }""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

case("外部包返回值直接站在子命令位上", "A20-3", SS, TAIL,
     TAIL + """

func bypassExtern() { _ = exec.Command("tmux", os.Getenv("REVIEW_COMMAND")) }""",
     "TestCommandArgumentsAreBuiltFromConstantsOnly")

# —— 跨包那条守卫：包外直接造命令必须红（K6 的反面，证明它确实扫到 internal/ui）——
UI_PLAIN_EXEC = ("package ui\n\nimport \"os/exec\"\n\n"
                 "func ctlPlainExec() { _ = exec.Command(\"tmux\", \"send-keys\") }\n")
case("adapter 包外直接写 exec.Command", "A21-1", "internal/ui/ctl_a21.go", None, UI_PLAIN_EXEC,
     "TestEachAdapterOnlyTouchesItsOwnUpstream", pkg="./internal/projectctl/")

# —— 已知缺口：注入后守边测试**必须仍然绿** ——
#
# owner 2026-09-23 裁定改按有限边界验收。下面是评审找到、这一版不修的绕过。
# 写成对照，清单就不靠嘴说：哪天某条变红，说明洞补上了，
# 去 4.eval/results/n037_workbench_attach/boundary-limits.md 把它挪出清单。
# 反过来，清单里写了、这里注入却变红，说明清单写错了。
GAPS = []
GAP_GUARD = GUARD + "|TestCommandArgumentsAreBuiltFromConstantsOnly"
def gap(desc, gid, edits, pkg="./internal/agentdeck/", pattern=GAP_GUARD):
    """edits 里 old 为 None 表示新建这个文件，还原时删掉。"""
    GAPS.append((desc, gid, edits, pkg, pattern))

SEND_KEYS = "string([]byte{115, 101, 110, 100, 45, 107, 101, 121, 115})"
BT = "internal/agentdeck/upstream_ast_boundary_test.go"

def tail(code):
    return [(SS, TAIL, TAIL + "\n\n" + code)]

gap("危险值放在第二个返回值", "K1", tail(
    "func opaquePair() (error, string) { return nil, " + SEND_KEYS + " }\n\n"
    "func gapSecondReturn() { _, x := opaquePair(); _ = exec.Command(\"tmux\", x) }"))

gap("经函数变量调用 wrapper", "K2a", tail(
    "func gapWrap(x string) { _ = exec.Command(\"tmux\", x) }\n\n"
    "func opaqueK2a() string { return " + SEND_KEYS + " }\n\n"
    "func gapFuncValue() { f := gapWrap; f(opaqueK2a()) }"))

gap("经接口变量调用 wrapper 方法", "K2b", tail(
    "type gapRunner interface{ Run(string) }\n\n"
    "type gapR struct{}\n\n"
    "func (gapR) Run(x string) { _ = exec.Command(\"tmux\", x) }\n\n"
    "func opaqueK2b() string { return " + SEND_KEYS + " }\n\n"
    "func gapIface() { var r gapRunner = gapR{}; r.Run(opaqueK2b()) }"))

gap("经接收者变量调用 wrapper 方法", "K2c", tail(
    "type gapR2 struct{}\n\n"
    "func (gapR2) Run(x string) { _ = exec.Command(\"tmux\", x) }\n\n"
    "func opaqueK2c() string { return " + SEND_KEYS + " }\n\n"
    "func gapRecv() { r := gapR2{}; r.Run(opaqueK2c()) }"))

gap("wrapper 先把入参装进切片再展开", "K3", tail(
    "func gapSliceWrap(x string) { a := []string{x}; _ = exec.Command(\"tmux\", a...) }\n\n"
    "func opaqueK3() string { return " + SEND_KEYS + " }\n\n"
    "func gapSlice() { gapSliceWrap(opaqueK3()) }"))

gap("字段原地改写", "K4a", tail(
    "type gapP struct{ Name string }\n\n"
    "func opaqueK4a() string { return " + SEND_KEYS + " }\n\n"
    "func gapField() { p := gapP{Name: \"list-sessions\"}; p.Name = opaqueK4a(); _ = exec.Command(\"tmux\", p.Name) }"))

gap("切片下标原地改写", "K4b", tail(
    "func opaqueK4b() string { return " + SEND_KEYS + " }\n\n"
    "func gapIndex() { a := []string{\"list-sessions\"}; a[0] = opaqueK4b(); _ = exec.Command(\"tmux\", a...) }"))

# 用 tmux 真实的全局布尔选项 -u。attempt 21 这里用的是 --no-wait，
# 评审实测它不是 tmux 选项（tmux 只打印 usage），这条对照于是没验证它声称的位置。
# 本机 tmux 3.4 实测：`tmux -L <不存在> -u list-sessions` 去连 server，即 -u 后一位就是命令。
gap("valueFlags 误列一个布尔 flag，后一位放进外部值", "K5",
    [(BT, '"--message-file": true,', '"--message-file": true, "-u": true,')] + tail(
    "func gapFlag() { _ = exec.Command(\"tmux\", \"-u\", os.Getenv(\"REVIEW_COMMAND\")) }"))

# 跨包那条守卫按文本找 "exec.Command"。给 os/exec 起个别名，它就看不见了。
gap("adapter 包外用别名导入 os/exec 直接造命令", "K6",
    [("internal/ui/gap_k6.go", None,
      "package ui\n\nimport ex \"os/exec\"\n\n"
      "func gapAliasExec() { _ = ex.Command(\"tmux\", \"send-keys\") }\n")],
    pkg="./internal/projectctl/", pattern="TestEachAdapterOnlyTouchesItsOwnUpstream")

# 同一条守卫按连续文本找。Go 允许选择器中间有空白，gofmt 之前这是合法代码。
gap("adapter 包外写 exec . Command（选择器中间带空白）", "K7",
    [("internal/ui/gap_k7.go", None,
      "package ui\n\nimport \"os/exec\"\n\n"
      "func gapSpacedExec() { _ = exec . Command(\"tmux\", \"send-keys\") }\n")],
    pkg="./internal/projectctl/", pattern="TestEachAdapterOnlyTouchesItsOwnUpstream")

def apply_edits(edits):
    """按顺序改文件，返回原文用于还原；改点找不到返回 None。"""
    originals = {}
    for rel, old, new in edits:
        p = WORK / rel
        if old is None:
            if p.exists():
                restore(originals)
                return None
            originals[rel] = None
            p.write_text(new)
            continue
        if rel not in originals:
            originals[rel] = p.read_text()
        cur = p.read_text()
        if old not in cur:
            restore(originals)
            return None
        p.write_text(cur.replace(old, new, 1))
    return originals

def restore(originals):
    for rel, text in originals.items():
        if text is None:
            (WORK / rel).unlink(missing_ok=True)
        else:
            (WORK / rel).write_text(text)

def main():
    ok_count = 0
    print("=== attempt 13 阳性对照 ===\n")
    for desc, blocker, rel, old, new, pattern, pkg in CASES:
        originals = apply_edits([(rel, old, new)])
        if originals is None:
            print(f"[跳过] {desc}：改点找不到，对照本身写坏了")
            continue
        try:
            red, out = run_test(pattern, pkg)
        finally:
            restore(originals)
        if red:
            ok_count += 1
            first = next((l for l in out.splitlines() if "client_test.go" in l or "的第" in l), "")
            print(f"[红] {blocker} {desc}")
            if first:
                print(f"      {first.strip()[:150]}")
        else:
            print(f"[没红！] {blocker} {desc}  ← 这条测试没在盯着它")
        sys.stdout.flush()
    print(f"\n{ok_count}/{len(CASES)} 处对照按预期变红")

    print("\n=== 已知缺口（注入后必须仍然绿）===\n")
    gap_ok = 0
    for desc, gid, edits, pkg, pattern in GAPS:
        originals = apply_edits(edits)
        if originals is None:
            print(f"[跳过] {gid} {desc}：改点找不到，缺口对照本身写坏了")
            continue
        try:
            touched = sorted({"./" + str(pathlib.Path(rel).parent) + "/" for rel, _, _ in edits})
            built = subprocess.run(["go", "vet"] + touched, cwd=WORK, env=ENV,
                                   capture_output=True, text=True)
            red, out = run_test(pattern, pkg)
        finally:
            restore(originals)
        if built.returncode != 0:
            # 注入代码编译不过，绿也不说明任何事——这就是假零。
            print(f"[编译失败] {gid} {desc}  ← 注入代码写错了，这条不算数")
            print("      " + (built.stdout + built.stderr).strip().splitlines()[-1][:150])
        elif red:
            print(f"[变红了] {gid} {desc}  ← 洞已补上，或清单写错了：更新 boundary-limits.md")
        else:
            gap_ok += 1
            print(f"[仍绿] {gid} {desc}")
        sys.stdout.flush()
    print(f"\n{gap_ok}/{len(GAPS)} 处已知缺口按清单仍然绿")
    return 0 if ok_count == len(CASES) and gap_ok == len(GAPS) else 1

sys.exit(main())
