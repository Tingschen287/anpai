#!/usr/bin/env python3
# n042 最小可用修复的阳性对照：逐条改坏修复，对应测试必须变红，跑完原样还原。
# n044 跟着代码改了两处锚点（开工改由派工器在 launch 之后调；任务文本按最终 attempt 生成），对照的意思不变。
# 改坏后编译不过的不算变红。真实二进制的对照要设 WORKDECK_AGENTDECK_BIN，没设就算没红（不假装跑过）。
# 用法：在 work/ 下 python3 scripts/mvp-positive-control.py
import os, subprocess, sys
W = os.environ.get('W') or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
env = dict(os.environ, PATH=os.path.expanduser('~/.local/go/bin') + ':' + os.environ['PATH'])
cases = [
 ("1 进入组件自己另开一份绑定", "main.go",
  "\tif storeErr != nil {\n\t\treturn nil, nil, storeErr\n\t}\n\treturn client, store, nil",
  "\tfresh, err := binding.Open(filepath.Join(filepath.Dir(path), \"bindings.json\"))\n\tif err != nil {\n\t\treturn nil, nil, err\n\t}\n\t_, _ = store, storeErr\n\treturn client, fresh, nil",
  ".", "TestDispatchedBindingIsVisibleToAttachWithoutRestart"),
 ("2 上游子进程继承 TMUX", "internal/agentdeck/client.go",
  "\tbase := withoutTmux(os.Environ())", "\tbase := os.Environ()",
  "./internal/arbiter/", "TestIntegrationDispatchWorksFromInsideAnotherTmuxServer"),
 ("2 客户端探测继承 TMUX", "internal/agentdeck/clients.go",
  "\tlist.Env = withoutTmux(os.Environ())\n", "\t_ = os.Environ\n",
  "./internal/arbiter/", "TestIntegrationDispatchWorksFromInsideAnotherTmuxServer"),
 ("3 声明无模型也照传 --model", "internal/dispatch/route.go",
  "\tif m.NoModel {\n\t\treturn \"\", true\n\t}", "\tif m.NoModel && false {\n\t\treturn \"\", true\n\t}",
  "./internal/dispatch/", "TestNoModelEntryLaunchesWithoutAModelFlag"),
 ("3 不核工具与模型的契约", "internal/agentdeck/launch.go",
  "\treturn CheckToolModel(r.Tool, r.ModelNotApplicable)", "\treturn nil",
  "./internal/agentdeck/", "TestToolModelContractIsCheckedBeforeLaunch"),
 ("4 不开工，照旧默认 attempt", "internal/ui/model.go",
  "\t\tif needStart {\n\t\t\treq.Attempt = 0\n\t\t\treq.Start = func",
  "\t\tif needStart && false {\n\t\t\treq.Attempt = 0\n\t\t\treq.Start = func",
  "./internal/ui/", "TestReadyNodeStartsInGraphAndUsesTheLedgerAttempt"),
 ("4 未知 attempt 也派", "internal/ui/model.go",
  "\tif !needStart && !hasAttempt {", "\tif false && !needStart && !hasAttempt {",
  "./internal/ui/", "TestReviewWithUnknownAttemptIsRefused"),
 ("4 Start 不核回执", "internal/projectctl/client.go",
  "\tif res.Attempt <= 0 || res.NodeID != r.NodeID {", "\tif false && (res.Attempt <= 0 || res.NodeID != r.NodeID) {",
  "./internal/projectctl/", "TestStartRejectsAMismatchedReceipt"),
 ("5 不认 delivery=typed", "internal/agentdeck/send.go",
  "rep.Delivery == DeliveryTyped {", "rep.Delivery == \"never\" {",
  "./internal/agentdeck/", "TestSendMessageReportsUnconfirmedDeliverySeparately"),
 ("5 送达未确认当失败丢掉", "internal/dispatch/dispatcher.go",
  "\tif err := d.Deck.SendMessage(ctx, res.SessionID, req.messageFor(attempt)); agentdeck.IsDeliveryUnconfirmed(err) {",
  "\tif err := d.Deck.SendMessage(ctx, res.SessionID, req.messageFor(attempt)); false && agentdeck.IsDeliveryUnconfirmed(err) {",
  "./internal/dispatch/", "TestUnconfirmedDeliveryStillBindsTheSession"),
 ("5 界面把送达未确认说成已送达", "internal/ui/model.go",
  "\t\tif b.Delivery == binding.DeliveryUnconfirmed {\n\t\t\treturn dispatchDoneMsg{ok: false, text: fmt.Sprintf(\n\t\t\t\t\"%s已派",
  "\t\tif false && b.Delivery == binding.DeliveryUnconfirmed {\n\t\t\treturn dispatchDoneMsg{ok: false, text: fmt.Sprintf(\n\t\t\t\t\"%s已派",
  "./internal/ui/", "TestUnconfirmedDeliveryIsFlaggedOnScreen"),
 ("1 派工回执不叠到快照（要等刷新）", "internal/ui/model.go",
  "\t\tif msg.nodeID != \"\" && msg.attempt > 0 {", "\t\tif false && msg.nodeID != \"\" && msg.attempt > 0 {",
  "./internal/ui/", "TestObserveRightAfterDispatchWithoutWaitingForRefresh"),
 ("1 晚到的旧快照盖掉派工结果", "internal/ui/model.go",
  "\t\tm.snap = projectctl.Snapshot(msg)\n\t\tm = m.applyOverlays()\n\t\tm.loading = false",
  "\t\tm.snap = projectctl.Snapshot(msg)\n\t\tm.loading = false",
  "./internal/ui/", "TestObserveRightAfterDispatchWithoutWaitingForRefresh"),
 ("6 句子中间的 VERDICT 也算结论", "internal/ui/verdict.go",
  "(?m)^[\\\\s•*>`-]*VERDICT:", "(?m)^.*VERDICT:",
  "./internal/ui/", "TestOnlyAStandaloneVerdictLineCounts"),
 ("4 ok 回执配非零退出也信", "internal/projectctl/client.go",
  "\tif runErr != nil {\n\t\treturn StartResult{}, fmt.Errorf(\"projectctl start 回执说成功",
  "\tif runErr != nil && false {\n\t\treturn StartResult{}, fmt.Errorf(\"projectctl start 回执说成功",
  "./internal/projectctl/", "TestStartDistrustsOkWithANonZeroExit"),
 ("5 上游成功退出就算送达（不看 submitted）", "internal/agentdeck/send.go",
  "\t\tif jsonErr == nil && rep.Submitted != nil && *rep.Submitted {", "\t\tif true {",
  "./internal/agentdeck/", "TestSendMessageReportsUnconfirmedDeliverySeparately"),
 ("5 队列把送达未确认当失败", "internal/queue/intake.go",
  "opt.Message); agentdeck.IsDeliveryUnconfirmed(err) {", "opt.Message); false && agentdeck.IsDeliveryUnconfirmed(err) {",
  "./internal/queue/", "TestUnconfirmedAnswerStaysPendingForOwnerCheck"),
 ("5 队列把送达未确认结案（attempt 4）", "internal/queue/intake.go",
  "\t\t\treturn s.MarkUnconfirmed(id, fmt.Sprintf(", "\t\t\tans.Delivered = true\n\t\t\t_ = s.MarkUnconfirmed\n\t\t\treturn s.Record(id, ans)\n\t\t\t_ = (fmt.Sprintf(",
  "./internal/queue/", "TestUnconfirmedAnswerStaysPendingForOwnerCheck"),
 ("6 取第一个而不是最后一个 VERDICT", "internal/ui/verdict.go",
  "\treturn all[len(all)-1][1]", "\treturn all[0][1]",
  "./internal/ui/", "TestVerdictIsShownWithACopyableReviewCommand"),
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
