#!/usr/bin/env bash
# 捕获嵌套 tmux 下「进入会话再返回」的证据。
#
# Work Deck 在这台机器上通常开在一个 tmux 窗格里，进入某个会话就成了两层。
# tmux 默认拒绝嵌套，这个脚本把拒绝、强制嵌套、返回三步的真实输出都抓下来。
#
# 用法：bash capture-nested-attach.sh [输出文件]
# 不依赖 Work Deck 二进制，验的是 adapter 所依赖的 tmux 行为本身。
set -uo pipefail

OUT="${1:-/dev/stdout}"
OUTER=wdcap_outer
INNER=wdcap_inner

cleanup() {
  tmux kill-session -t "$OUTER" 2>/dev/null
  tmux kill-session -t "$INNER" 2>/dev/null
}
trap cleanup EXIT

cap() { tmux capture-pane -t "$OUTER" -p 2>/dev/null | grep -vE '^\s*$' | tail -"${1:-4}"; }

{
  echo "# 嵌套 tmux 进入与返回捕获"
  echo
  echo "捕获时间：$(date -u +%Y-%m-%dT%H:%M:%S+00:00)"
  echo "tmux 版本：$(tmux -V)"
  echo
  echo "外层会话 \`$OUTER\` 模拟 Work Deck 所在的窗格，内层 \`$INNER\` 模拟被进入的 agent 会话。"
  echo

  cleanup
  sleep 1
  tmux new-session -d -s "$INNER" 'echo ===INNER_MARKER===; sleep 600'
  tmux new-session -d -s "$OUTER"
  sleep 1

  echo "## 0. 起始状态"
  echo
  echo '```'
  tmux ls
  echo '```'
  echo

  echo "## 1. 不清 TMUX 直接进入 — tmux 拒绝"
  echo
  echo "这是 Work Deck 不做任何处理时会撞到的情况。"
  echo
  echo '```'
  tmux send-keys -t "$OUTER" 'tmux attach-session -t '"$INNER"'; echo "退出码=$?"' Enter
  sleep 2
  cap 3
  echo '```'
  echo

  echo "## 2. 清掉 TMUX 后进入 — 成功嵌套"
  echo
  echo "这正是 adapter 的 \`prepareNested\` 所做的：发现自己在 tmux 里，就把 TMUX"
  echo "与 TMUX_PANE 从子进程环境里去掉。"
  echo
  echo '```'
  tmux send-keys -t "$OUTER" 'env -u TMUX tmux attach-session -t '"$INNER" Enter
  sleep 3
  cap 3
  echo '```'
  echo
  echo "外层窗格显示的是内层的内容和内层的状态栏，说明确实进去了。"
  echo

  echo "## 3. 返回"
  echo
  echo '```'
  tmux send-keys -t "$OUTER" C-b
  sleep 0.4
  tmux send-keys -t "$OUTER" d
  sleep 2
  cap 4
  echo '```'
  echo
  echo "回到了外层的命令行。内层会话仍然活着，没有被杀掉："
  echo
  echo '```'
  tmux ls
  echo '```'
  echo

  echo "## 关于前缀键：自动化证明不了的那部分"
  echo
  echo "上面第 3 步只发了**一次**前缀键就返回了，但真人在终端里要按**两次**。"
  echo
  echo "原因是两条输入路径不同："
  echo
  echo "- \`tmux send-keys\` 把按键直接注入目标窗格，内层 client 第一个收到，"
  echo "  于是第一次 \`C-b\` 就被内层当成自己的前缀。"
  echo "- 真人敲键盘时，外层 tmux server 先收到，第一次 \`C-b\` 会被外层吃掉，"
  echo "  要按第二次才穿透到内层。"
  echo
  echo "所以这份捕获能证明**进得去、回得来、内层会话不受损**，"
  echo "但证明不了真人按键的手感。那一步需要在真实终端里人工确认。"
} > "$OUT"
