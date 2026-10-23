package deckui

// sayStartMain 把「起主 agent 的结果」交给侧栏循环（和 RunSidebar 里收到 startedMain 时做的一样）。
// 单独放一个文件：返工 2 把 loop.say 改成收 lazyText，这一个函数是测试里唯一接触该签名的地方。
func sayStartMain(l *loop, project string, res StartMainResult, err error) {
	text, ok := startMainNote(project, res, err)
	l.say(text, ok)
}
