import subprocess, os, sys
# 决策队列的阳性对照：逐条改坏实现，对应测试必须变红，跑完原样还原。
# 用法：python3 scripts/queue-positive-control.py（在 work/ 下）
W=os.environ.get('W') or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
env=dict(os.environ, PATH=os.path.expanduser('~/.local/go/bin')+':'+os.environ['PATH'])
cases=[
 ("去重键带上时间戳","internal/queue/build.go",'id := "session:" + r.Fingerprint()[:24]','id := "session:" + r.Fingerprint()[:12] + fmt.Sprintf("%012x", r.Timestamp.UnixNano()&0xffffffffffff)',"./internal/queue/","TestDuplicateReportsDedupe"),
 ("指纹不做大小写规范化","internal/agentdeck/inbox.go","\tb.WriteString(strings.ToLower(strings.TrimSpace(r.ToStatus)))","\tb.WriteString(strings.TrimSpace(r.ToStatus))","./internal/agentdeck/","TestFingerprintNormalizesLikeUpstream"),
 ("daemon 不在时不告警","internal/queue/intake.go","\tcase !in.Writer.Running:","\tcase false:","./internal/queue/","TestPollChecksWriterFirstAndAlertsLoudly"),
 ("心跳新鲜但进程不在时不告警","internal/queue/intake.go","\tcase !in.ProcessUp:","\tcase false:","./internal/queue/","TestPollChecksWriterFirstAndAlertsLoudly"),
 ("进程表只看参数不看程序名","internal/agentdeck/inbox.go",'\t\tif !strings.Contains(filepath.Base(argv[0]), "agent-deck") {\n\t\t\tcontinue\n\t\t}','',"./internal/agentdeck/","TestNotifyDaemonRunningReadsTheProcessTable"),
 ("回答不落盘","internal/queue/store.go","\tif err := s.flushLocked(); err != nil {\n\t\treturn Item{}, err\n\t}\n\treturn *it, nil","\treturn *it, nil","./internal/queue/","TestQueueSurvivesRestart"),
 ("缺 items 的文件当空队列","internal/queue/store.go","\tif f.Items == nil {\n\t\treturn nil, bad(\"缺少 items 字段\")\n\t}","\tif f.Items == nil {\n\t\treturn s, nil\n\t}","./internal/queue/","TestCorruptQueueFileIsNotAnEmptyQueue"),
 ("投递给父会话而不是发起会话","internal/queue/intake.go","rec := agentdeck.InboxRecord{ChildSessionID: it.Source.SessionID,","rec := agentdeck.InboxRecord{ChildSessionID: \"parent-1\",","./internal/queue/","TestAnswerDeliversToOriginatingSession"),
 ("已回答的会话条目还能再投","internal/queue/intake.go","\tif it.Source.Kind == SourceSession && it.Status == StatusAnswered {","\tif false {","./internal/queue/","TestAnsweredSessionItemIsNotDeliveredTwice"),
 ("投递中断后自动重投","internal/queue/intake.go","\tif it.Answer != nil && it.Answer.InFlight {","\tif false {","./internal/queue/","TestInterruptedDeliveryIsNotRetried"),
 ("Graph 条目选了就出待拍板","internal/queue/store.go","\tif a.DeliveryError == \"\" && it.Source.Kind == SourceSession {","\tif a.DeliveryError == \"\" {","./internal/queue/","TestGraphAnswerOnlyHandsOverCommand"),
 ("收下别的 profile 的记录","internal/queue/intake.go","\t\tif r.Profile != \"\" && r.Profile == in.Profile {","\t\tif true {","./internal/queue/","TestPollKeepsOnlyOwnProfileRecords"),
 ("放行 inbox drain","internal/agentdeck/client.go",'"inbox export", "inbox writer-status"}','"inbox export", "inbox writer-status", "inbox drain"}',"./internal/agentdeck/","TestInboxOnlyAllowsNonConsumingReads"),
 ("主界面来源不可信时仍说队列为空","internal/ui/queue.go",'\tcase q.Alert != "" || q.Healthy == "":\n\t\tw("%s", styleDim.Render("决策队列暂无条目','\tcase false:\n\t\tw("%s", styleDim.Render("决策队列暂无条目',"./internal/ui/","TestMainScreenAlertsWhenDaemonIsNotRunning"),
 ("队列文件打不开时界面不告警","internal/ui/queue.go",'\tif m.qw.err != "" {\n\t\treturn "决策队列打不开','\tif false {\n\t\treturn "决策队列打不开',"./internal/ui/","TestQueueFileErrorIsAStandingAlert"),
 ("--once 不画队列","main.go","\tq, open := onceQueue(ctx, snap, script)\n\treturn ui.RenderWithQueue(snap, width, q, open)","\treturn ui.Render(snap, width)","./","TestOnceRenderCarriesTheQueueAlert"),
 ("任意键都当确认","internal/ui/queue.go",'\t\tcase "y":\n\t\t\tid, opt','\t\tcase "y", "n":\n\t\t\tid, opt',"./internal/ui/","TestAnyOtherKeyCancelsThePendingAnswer"),
 ("Graph 门不进队列","internal/queue/build.go",'\t\tcase n.Kind == "human_gate" && (st == projectctl.StateReady || st == projectctl.StateRunning):','\t\tcase false:',"./internal/queue/","TestGraphAndSessionItemsShareOneQueue"),
]
red=0
for desc,path,old,new,pkg,pat in cases:
    p=os.path.join(W,path); src=open(p).read()
    assert src.count(old)==1,(desc,old[:40])
    open(p,'w').write(src.replace(old,new))
    try:
        r=subprocess.run(["go","test",pkg,"-count=1","-run","^"+pat+"$"],cwd=W,env=env,capture_output=True,text=True)
    finally:
        open(p,'w').write(src)
    ok = r.returncode!=0 and "FAIL" in r.stdout and "build failed" not in r.stdout and "[setup failed]" not in r.stdout
    red+=ok
    print(("[变红] " if ok else "[没红] ")+desc+"  ("+pat+")")
    if not ok: print(r.stdout[-600:])
print(f"{red}/{len(cases)} 处对照按预期变红")
sys.exit(0 if red==len(cases) else 1)
