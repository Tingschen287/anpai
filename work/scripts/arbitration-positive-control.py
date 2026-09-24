#!/usr/bin/env python3
# 写入仲裁与能力路由的阳性对照（n039）：逐条改坏实现，对应测试必须变红，跑完原样还原。
# 改坏后编译不过的不算变红。用法：python3 scripts/arbitration-positive-control.py（在 work/ 下）
import os, subprocess, sys
W = os.environ.get('W') or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
env = dict(os.environ, PATH=os.path.expanduser('~/.local/go/bin') + ':' + os.environ['PATH'])
cases = [
 ("SendMessage 不核许可", "internal/agentdeck/send.go",
  "\tif err := p.Allows(sessionID); err != nil {\n\t\treturn fmt.Errorf(\"不投递：%w\", err)\n\t}",
  "\t_ = p", "./internal/agentdeck/", "TestSendMessageRefusesAZeroPermit"),
 ("adapter 另开一条往会话写字的路", "internal/agentdeck/send.go",
  "func (c *Client) SendMessage(",
  "func (c *Client) sneakySend(ctx context.Context, id, m string) error {\n\t_, err := c.execWithStdin(ctx, strings.NewReader(m), \"session\", \"send\", id, \"--message-file\", \"-\")\n\treturn err\n}\n\nfunc (c *Client) SendMessage(",
  "./internal/agentdeck/", "TestEveryWriteIntoASessionNeedsAPermit"),
 ("owner 接管时仍签自动许可", "internal/lease/permit.go",
  "\t\tcase l.Holder == HolderOwner:\n\t\t\treturn fmt.Errorf(\"会话 %s 正由 owner 接管",
  "\t\tcase false:\n\t\t\treturn fmt.Errorf(\"会话 %s 正由 owner 接管",
  "./internal/lease/", "TestOwnerTakeoverStopsAutomaticDelivery"),
 ("签出后不再复核", "internal/lease/permit.go",
  "\tp.store.mu.Lock()\n\tdefer p.store.mu.Unlock()\n\treturn p.store.checkLocked(sessionID, p.kind, p.store.now())",
  "\treturn nil", "./internal/lease/", "TestOwnerTakeoverStopsAutomaticDelivery"),
 ("回来后不等确认就恢复", "internal/lease/permit.go",
  "\t\tcase l.Holder == HolderOwner && l.PendingHandback != nil:",
  "\t\tcase false && l.PendingHandback != nil:", "./internal/lease/", "TestHandbackRequiresConfirmation"),
 ("租约过期不拦", "internal/lease/permit.go",
  "\t\tcase l.ExpiresAt.IsZero() || !now.Before(l.ExpiresAt):",
  "\t\tcase false:", "./internal/lease/", "TestExpiredLeaseFailsClosed"),
 ("没记录也放行自动投递", "internal/lease/permit.go",
  "\t\tcase !ok:\n\t\t\treturn fmt.Errorf(\"会话 %s 没有写入权记录",
  "\t\tcase false:\n\t\t\treturn fmt.Errorf(\"会话 %s 没有写入权记录",
  "./internal/lease/", "TestAutomaticNeedsAWorkbenchLease"),
 ("落盘失败不停用", "internal/lease/lease.go",
  "\tif err := s.flushLocked(); err != nil {\n\t\ts.restore(sessionID, prev, had)\n\t\ts.broken = err",
  "\tif err := s.flushLocked(); err != nil {\n\t\ts.restore(sessionID, prev, had)",
  "./internal/lease/", "TestFlushFailureStopsAllDelivery"),
 ("切换不写事件", "internal/lease/lease.go",
  "\tif err := s.appendEventLocked(ev); err != nil {",
  "\tif err := error(nil); err != nil {", "./internal/lease/", "TestEverySwitchIsAnEvent"),
 ("坏记账当空记账", "internal/lease/lease.go",
  "\tif f.Leases == nil {\n\t\treturn nil, bad(\"缺少 leases 字段\")\n\t}",
  "\tif f.Leases == nil {\n\t\treturn s, nil\n\t}", "./internal/lease/", "TestLeasesSurviveRestartAndBadFilesAreErrors"),
 ("Grant 能从 owner 手里夺回", "internal/lease/lease.go",
  "\tif l, ok := s.leases[sessionID]; ok && (l.Holder == HolderOwner || l.PendingHandback != nil) {",
  "\tif l, ok := s.leases[sessionID]; ok && false && (l.Holder == HolderOwner || l.PendingHandback != nil) {",
  "./internal/lease/", "TestOwnerTakeoverStopsAutomaticDelivery"),
 ("arbiter 自动投递用手动许可", "internal/arbiter/arbiter.go",
  "\tp, err := d.Leases.Automatic(sessionID)", "\tp, err := d.Leases.Manual(sessionID)",
  "./internal/arbiter/", "TestManualAnswerRules"),
 ("界面进入前不登记接管", "internal/ui/lease.go",
  "\t\tif err := m.lw.store.OwnerTakeover(sessionID, via); err != nil {",
  "\t\tif err := error(nil); err != nil {", "./internal/ui/", "TestAttachRecordsTheOwnerTakeoverFirst"),
 ("H 之后任意键都确认", "internal/ui/lease.go",
  "\tif key != \"y\" {", "\tif false {", "./internal/ui/", "TestHandbackNeedsTheOwnersConfirmation"),
 ("能力按工具名推断", "internal/routing/routing.go",
  "\t\t\tv, ok := c.Caps[cap]\n\t\t\tif !ok || (v != Yes && v != No) {\n\t\t\t\tv = Unknown\n\t\t\t}",
  "\t\t\tv, ok := c.Caps[cap]\n\t\t\tif !ok && c.Tool == \"claude\" {\n\t\t\t\tv, ok = Yes, true\n\t\t\t}\n\t\t\tif !ok || (v != Yes && v != No) {\n\t\t\t\tv = Unknown\n\t\t\t}",
  "./internal/routing/", "TestCapabilitiesAreNeverInferredFromTheToolName"),
 ("额度未知当可用", "internal/routing/routing.go",
  "\t\t\tif f.Quota.State != QuotaAvailable {", "\t\t\tif f.Quota.State == QuotaExhausted {",
  "./internal/routing/", "TestUnknownQuotaEscalatesToOwner"),
 ("低档也可选", "internal/routing/routing.go",
  "\t\tcase !f.MeetsRouteClass:\n", "\t\tcase false:\n",
  "./internal/routing/", "TestRoutingNeverLowersTheRouteClass"),
 ("失败的完成也算额度可用", "internal/routing/quota.go",
  "\t\tcase it.Category == queue.CatSessionFinished && it.Source.DoneStatus == \"ok\":",
  "\t\tcase it.Category == queue.CatSessionFinished:",
  "./internal/routing/", "TestQuotaComesOnlyFromWorkbenchObservations"),
 ("审计记录写失败照样派", "internal/dispatch/dispatcher.go",
  "\tif err := routing.Append(d.RoutingLog, dec); err != nil {",
  "\tif err := routing.Append(d.RoutingLog, dec); err != nil && false {",
  "./internal/dispatch/", "TestEveryDispatchLeavesARoutingRecord"),
]
red = 0
for desc, path, old, new, pkg, pat in cases:
    p = os.path.join(W, path); src = open(p).read()
    assert src.count(old) == 1, (desc, old[:50])
    open(p, 'w').write(src.replace(old, new))
    try:
        r = subprocess.run(["go", "test", pkg, "-count=1", "-run", "^" + pat + "$"], cwd=W, env=env, capture_output=True, text=True)
    finally:
        open(p, 'w').write(src)
    ok = r.returncode != 0 and "FAIL" in r.stdout and "build failed" not in r.stdout and "[setup failed]" not in r.stdout
    red += ok
    print(("[变红] " if ok else "[没红] ") + desc + "  (" + pat + ")")
    if not ok:
        print(r.stdout[-600:])
print(f"{red}/{len(cases)} 处对照按预期变红")
sys.exit(0 if red == len(cases) else 1)
