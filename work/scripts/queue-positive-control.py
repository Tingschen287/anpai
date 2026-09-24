import subprocess, os, sys
# 决策队列的阳性对照：逐条改坏实现，对应测试必须变红，跑完原样还原。
# 用法：python3 scripts/queue-positive-control.py（在 work/ 下）
W=os.environ.get('W') or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
env=dict(os.environ, PATH=os.path.expanduser('~/.local/go/bin')+':'+os.environ['PATH'])
cases=[
 ("去重键带上时间戳","internal/queue/build.go",'id := "session:" + r.Fingerprint()[:24]','id := "session:" + r.Fingerprint()[:12] + fmt.Sprintf("%012x", r.Timestamp.UnixNano()&0xffffffffffff)',"./internal/queue/","TestDuplicateReportsDedupe"),
 ("daemon 不在时不告警","internal/queue/intake.go","\tcase !in.Writer.Running:","\tcase false:","./internal/queue/","TestPollChecksWriterFirstAndAlertsLoudly"),
 ("回答不落盘","internal/queue/store.go","\tif err := s.flushLocked(); err != nil {\n\t\treturn Item{}, err\n\t}\n\treturn *it, nil","\treturn *it, nil","./internal/queue/","TestQueueSurvivesRestart"),
 ("投递给父会话而不是发起会话","internal/queue/intake.go","rec := agentdeck.InboxRecord{ChildSessionID: it.Source.SessionID,","rec := agentdeck.InboxRecord{ChildSessionID: \"parent-1\",","./internal/queue/","TestAnswerDeliversToOriginatingSession"),
 ("坏文件当空队列","internal/queue/store.go","\t\treturn nil, fmt.Errorf(\"队列文件 %s 解析不了","\t\treturn s, nil\n\t\treturn nil, fmt.Errorf(\"队列文件 %s 解析不了","./internal/queue/","TestCorruptQueueFileIsNotAnEmptyQueue"),
 ("放行 inbox drain","internal/agentdeck/client.go",'"inbox export", "inbox writer-status"}','"inbox export", "inbox writer-status", "inbox drain"}',"./internal/agentdeck/","TestInboxOnlyAllowsNonConsumingReads"),
 ("主界面告警时仍说队列为空","internal/ui/queue.go",'\tcase q.Alert != "":\n\t\tw("%s", styleDim.Render("决策队列暂无条目','\tcase false:\n\t\tw("%s", styleDim.Render("决策队列暂无条目',"./internal/ui/","TestMainScreenAlertsWhenDaemonIsNotRunning"),
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
