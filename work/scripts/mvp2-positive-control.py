#!/usr/bin/env python3
# n044 最小可用修复第二轮的阳性对照：逐条改坏修复，对应测试必须变红，跑完原样还原。
# 覆盖 n043 复审新问题 1–4（shell 不接任务、先起会话再开工、结论取自报告、Codex trust 先问）与 5（退出提示）。
# 改坏后编译不过的不算变红。真实二进制的对照要设 WORKDECK_AGENTDECK_BIN，没设就算没红（不假装跑过）。
# 用法：在 work/ 下 python3 scripts/mvp2-positive-control.py
import os, subprocess, sys
W = os.environ.get('W') or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
env = dict(os.environ, PATH=os.path.expanduser('~/.local/go/bin') + ':' + os.environ['PATH'])
E2E = "TestIntegrationEndToEndDispatchAgainstRealUpstream"
cases = [
 # 1 shell 不接任务
 ("1 配置写 shell 也不在 launch 前拒（判据失效）", "internal/agentdeck/manifest.go",
  "\tcase contains(ToolsWithoutTaskText, tool):", "\tcase false && contains(ToolsWithoutTaskText, tool):",
  "./internal/dispatch/", "TestShellIsRefusedBeforeAnythingHappens"),
 ("1 派工器 launch 前不核工具", "internal/dispatch/dispatcher.go",
  "\tif err := agentdeck.CheckTakesTaskText(ToolOf(model), model.DeclaredAgent); err != nil {\n\t\treturn refuse(err)\n\t}\n\n\t// 同一次",
  "\t_ = agentdeck.CheckTakesTaskText\n\n\t// 同一次",
  "./internal/dispatch/", "TestShellIsRefusedBeforeAnythingHappens"),
 ("1 评审这条路不核工具", "internal/dispatch/review.go",
  "\tif err := agentdeck.CheckTakesTaskText(ToolOf(model), model.DeclaredAgent); err != nil {\n\t\treturn refuse(err)\n\t}",
  "\t_ = agentdeck.CheckTakesTaskText",
  "./internal/dispatch/", "TestShellIsRefusedBeforeAnythingHappens"),
 # attempt 2（n044 attempt 1 评审）
 ("1 自定义工具没声明 agent 也放行", "internal/agentdeck/manifest.go",
  "\tcase !declaredAgent:", "\tcase false && !declaredAgent:",
  "./internal/dispatch/", "TestCustomToolNeedsAnAgentDeclarationAndARealAgentCommand"),
 ("1 不认实际命令是 shell 解释器", "internal/agentdeck/manifest.go",
  "\tif bin := commandBinary(cmd); contains(ShellInterpreters, bin) {", "\tif bin := commandBinary(cmd); false && contains(ShellInterpreters, bin) {",
  "./internal/dispatch/", "TestCustomToolNeedsAnAgentDeclarationAndARealAgentCommand"),
 ("1 不认实际命令是 shell 解释器（真实二进制）", "internal/agentdeck/manifest.go",
  "\tif bin := commandBinary(cmd); contains(ShellInterpreters, bin) {", "\tif bin := commandBinary(cmd); false && contains(ShellInterpreters, bin) {",
  "./internal/dispatch/", E2E),
 ("1 认不出 env 前缀", "internal/agentdeck/manifest.go",
  "\t\tif base == \"env\" {", "\t\tif false && base == \"env\" {",
  "./internal/agentdeck/", "TestToolsThatAreNotAgentsGetNoTaskText"),
 ("1 launch 后不按回执核工具（伪装的 shell 放过去）", "internal/dispatch/dispatcher.go",
  "\tif err := agentdeck.CheckLaunchedTakesTaskText(res); err != nil {\n\t\tif req.Start != nil {",
  "\tif err := agentdeck.CheckLaunchedTakesTaskText(res); false && err != nil {\n\t\tif req.Start != nil {",
  "./internal/dispatch/", "TestDisguisedShellIsStoppedBeforeStart"),
 ("1 launch 后不按回执核工具（真实二进制）", "internal/dispatch/dispatcher.go",
  "\tif err := agentdeck.CheckLaunchedTakesTaskText(res); err != nil {\n\t\tif req.Start != nil {",
  "\tif err := agentdeck.CheckLaunchedTakesTaskText(res); false && err != nil {\n\t\tif req.Start != nil {",
  "./internal/dispatch/", E2E),
 ("1 回执没写工具也信", "internal/agentdeck/manifest.go",
  "\tif strings.TrimSpace(res.Tool) == \"\" {", "\tif strings.TrimSpace(res.Tool) == \"\" && false {",
  "./internal/agentdeck/", "TestToolsThatAreNotAgentsGetNoTaskText"),
 # 2 先起会话、再开工、最后送任务；没拿到任务的会话停掉
 ("2 开工挪到 launch 之前", "internal/dispatch/dispatcher.go",
  "\tout.RoutingAt = routedAt\n\n\tlm, na := launchModel(model)",
  "\tout.RoutingAt = routedAt\n\tif req.Start != nil {\n\t\tif _, err := req.Start(ctx); err != nil {\n\t\t\treturn refuse(err)\n\t\t}\n\t}\n\n\tlm, na := launchModel(model)",
  "./internal/dispatch/", "TestStartHappensAfterLaunchAndBeforeSend"),
 ("2 launch 失败也开工", "internal/dispatch/dispatcher.go",
  "\t\tif req.Start != nil {\n\t\t\treturn refuse(fmt.Errorf(\"upstream agent-deck 启动会话失败",
  "\t\tif req.Start != nil {\n\t\t\t_, _ = req.Start(ctx)\n\t\t\treturn refuse(fmt.Errorf(\"upstream agent-deck 启动会话失败",
  "./internal/dispatch/", "TestLaunchFailureNeverStartsTheGraph"),
 ("2 开工失败不停会话", "internal/dispatch/dispatcher.go",
  "\t\t\treturn abandon(fmt.Errorf(\"Graph 开工失败（确定没开工，节点状态不变）：%w\", err))",
  "\t\t\treturn refuse(fmt.Errorf(\"Graph 开工失败（确定没开工，节点状态不变）：%w\", err))",
  "./internal/dispatch/", "TestStartFailureStopsTheFreshSession"),
 ("2 开工失败不停会话（真实二进制）", "internal/dispatch/dispatcher.go",
  "\t\t\treturn abandon(fmt.Errorf(\"Graph 开工失败（确定没开工，节点状态不变）：%w\", err))",
  "\t\t\treturn refuse(fmt.Errorf(\"Graph 开工失败（确定没开工，节点状态不变）：%w\", err))",
  "./internal/dispatch/", E2E),
 ("2 任务送不进去不停会话", "internal/dispatch/dispatcher.go",
  "\t\treturn abandon(cause)\n\t}\n\n\tb := binding.Binding{",
  "\t\treturn refuse(cause)\n\t}\n\n\tb := binding.Binding{",
  "./internal/dispatch/", "TestSendFailureAfterStartStopsTheSessionAndSaysRunning"),
 ("2 停会话不核许可", "internal/agentdeck/stop.go",
  "\trelease, err := p.Begin(sessionID)\n\tif err != nil {\n\t\treturn fmt.Errorf(\"不停会话：%w\", err)\n\t}\n\tdefer release()",
  "\t_ = p",
  "./internal/agentdeck/", "TestStopSessionRefusesAZeroPermit"),
 ("2 停会话沿用调用方已取消的 context", "internal/dispatch/dispatcher.go",
  "\tsctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonTimeout)", "\tsctx, cancel := context.WithTimeout(ctx, abandonTimeout)",
  "./internal/dispatch/", "TestStartTimeoutStillStopsTheFreshSession"),
 # attempt 3（n044 attempt 2 评审）
 ("2 旧绑定直接丢掉、不留档", "internal/binding/store.go",
  "\ts.superseded = append(s.superseded, Superseded{Binding: old, SupersededAt: at, By: by, Reason: reason})", "\t_ = at",
  "./internal/dispatch/", "TestStaleBindingForTheStartedAttemptIsSupersededNotLost"),
 ("2 旧绑定挪出后原键不腾空（送任务失败时补派被挡）", "internal/binding/store.go",
  "\tdelete(s.items, key)\n\ts.superseded", "\t_ = key\n\ts.superseded",
  "./internal/dispatch/", "TestStaleBindingIsMovedBeforeSendSoRedispatchStaysPossible"),
 ("2 开工结果不明也说没开工", "internal/dispatch/dispatcher.go",
  "\t\tcase err != nil:\n\t\t\treturn abandon(&StartUncertainError{Cause: err})",
  "\t\tcase err != nil:\n\t\t\treturn abandon(fmt.Errorf(\"Graph 开工失败（确定没开工，节点状态不变）：%w\", err))",
  "./internal/dispatch/", "TestStartFailureStopsTheFreshSession"),
 ("2 开工出岔子不对账、一律算没开工", "internal/projectctl/client.go",
  "\tif after.Events == before {", "\tif true {",
  "./internal/projectctl/", "TestIntegrationStartReconcilesWhenTheReceiptGoesWrong"),
 ("2 对账不核 actor", "internal/projectctl/client.go",
  "p.States[r.NodeID] == StateRunning && p.Actors[r.NodeID] == r.Actor &&", "p.States[r.NodeID] == StateRunning &&",
  "./internal/projectctl/", "TestIntegrationStartReconcilesWhenTheReceiptGoesWrong"),
 ("2 开工前核对不了账本也开工", "internal/projectctl/client.go",
  "\tbefore, err := c.VerifyChain(ctx)\n\tif err != nil {", "\tbefore, err := c.VerifyChain(ctx)\n\tif err != nil && false {",
  "./internal/projectctl/", "TestStartRefusesWhenTheLedgerCannotBeCheckedFirst"),
 ("2 界面把开工结果不明说成被拒", "internal/ui/model.go",
  "\t\t\tif dispatch.IsStartUncertain(err) {", "\t\t\tif false && dispatch.IsStartUncertain(err) {",
  "./internal/ui/", "TestStartFailureStopsDispatchAndDispatchFailureSaysHowToRecover"),
 ("2 界面不点名被顶替的旧会话", "internal/ui/model.go",
  "\t\tif b.Supersedes != \"\" {", "\t\tif false && b.Supersedes != \"\" {",
  "./internal/ui/", "TestSupersededBindingIsCalledOutOnScreen"),
 ("2 停会话收手动许可", "internal/agentdeck/stop.go",
  "\tif p.Kind() != lease.KindAutomatic {", "\tif false && p.Kind() != lease.KindAutomatic {",
  "./internal/agentdeck/", "TestStopSessionRunsSessionStopOnlyWithAPermit"),
 ("2 有人连着也停", "internal/arbiter/arbiter.go",
  "\tif n > 0 {\n\t\treason := fmt.Sprintf(\"检测到 %d 个 tmux 客户端连着会话",
  "\tif false && n > 0 {\n\t\treason := fmt.Sprintf(\"检测到 %d 个 tmux 客户端连着会话",
  "./internal/arbiter/", "TestAbandonStopsOnlyAnUnattachedSessionWorkDeckHolds"),
 # 3 结论取自报告文件
 ("3 没有报告就退回读画面", "internal/ui/verdict.go",
  "\tdata, err := os.ReadFile(filepath.Join(m.snap.ProjectRoot, report))\n\tif err != nil {",
  "\tdata, err := os.ReadFile(filepath.Join(m.snap.ProjectRoot, report))\n\tif err != nil && m.attacher != nil {\n\t\tif p0, perr := m.attacher.PaneOutputPlain(context.Background(), b.SessionID); perr == nil {\n\t\t\tdata, err = []byte(p0), nil\n\t\t}\n\t}\n\tif err != nil {",
  "./internal/ui/", "TestAVerdictOnlyOnThePaneGivesNoCommand"),
 ("3 详情不给报告哈希", "internal/ui/verdict.go",
  "\tfmt.Fprintf(&d, \"报告：%s（sha256 %s，%d 字节\", report, sha, len(data))",
  "\tfmt.Fprintf(&d, \"报告：%s（%d 字节\", report, len(data))",
  "./internal/ui/", "TestVerdictIsShownWithACopyableReviewCommand"),
 ("3 不说这不是验收事实", "internal/ui/verdict.go",
  "\td.WriteString(notAFact + \"\\n\")", "\td.WriteString(\"\\n\")",
  "./internal/ui/", "TestVerdictIsShownWithACopyableReviewCommand"),
 ("3 记账理由预先声称已核对", "internal/ui/verdict.go",
  "结论 VERDICT: %s；由 owner 核对报告后运行本命令记账", "结论 VERDICT: %s；owner 已核对报告",
  "./internal/ui/", "TestVerdictIsShownWithACopyableReviewCommand"),
 ("3 画面与报告不一致不警告", "internal/ui/verdict.go",
  "\t\t\tif pv != verdict {", "\t\t\tif false && pv != verdict {",
  "./internal/ui/", "TestPaneVerdictIsOnlyAHint"),
 # 4 派 codex 之前先问
 ("4 codex 不问就派", "internal/ui/model.go",
  "\t\tif w := codexTrustWarning(m.candidates[m.pick], m.snap.ProjectRoot); w != \"\" {",
  "\t\tif w := codexTrustWarning(m.candidates[m.pick], m.snap.ProjectRoot); false && w != \"\" {",
  "./internal/ui/", "TestCodexDispatchWaitsForTheOwnerToConfirmTheTrustWrite"),
 ("4 确认这一步乱按也派", "internal/ui/model.go",
  "\tcase \"y\":\n\t\tm.askingTrust, m.trustWarning = false, \"\"",
  "\tcase \"y\", \"x\":\n\t\tm.askingTrust, m.trustWarning = false, \"\"",
  "./internal/ui/", "TestCodexDispatchWaitsForTheOwnerToConfirmTheTrustWrite"),
 ("4 认不出的自定义工具当成不写", "internal/agentdeck/manifest.go",
  "\tdefault:\n\t\treturn CodexTrustMaybe", "\tdefault:\n\t\treturn CodexTrustNo",
  "./internal/ui/", "TestTrustStepOnlyForToolsThatMayWriteIt"),
 # 5 退出提示看外层 server
 ("5 不分外层是哪台 server", "internal/agentdeck/nested.go",
  "\treturn filepath.Base(sock) == \"default\"", "\treturn filepath.Base(sock) == \"default\" || true",
  "./internal/agentdeck/", "TestNestingAdviceFollowsTheOuterServer"),
]
red = 0
for desc, path, old, new, pkg, pat in cases:
    p = os.path.join(W, path); src = open(p).read()
    assert src.count(old) == 1, (desc, old[:60])
    open(p, 'w').write(src.replace(old, new))
    try:
        r = subprocess.run(["go", "test", pkg, "-count=1", "-run", "^" + pat + "$", "-v"], cwd=W, env=env, capture_output=True, text=True)
    finally:
        open(p, 'w').write(src)
    ok = (r.returncode != 0 and "--- FAIL" in r.stdout and "build failed" not in r.stdout
          and "[setup failed]" not in r.stdout)
    red += ok
    print(("[变红] " if ok else "[没红] ") + desc + "  (" + pat + ")")
    if not ok:
        print(r.stdout[-600:])
print(f"{red}/{len(cases)} 处对照按预期变红")
sys.exit(0 if red == len(cases) else 1)
