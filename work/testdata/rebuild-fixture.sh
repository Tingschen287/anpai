#!/usr/bin/env bash
# 重建 Work Deck 的 Graph 夹具。
#
# 夹具是一个假项目，账本由 projectctl 亲自写出，所以它和真实项目的账本同构。
# 七个节点分别停在 accepted / running / submitted / blocked / failed / ready / planned，
# 让读取方在一个账本上看到全部状态分支。
#
# 用法：bash rebuild-fixture.sh
# 依赖：projectctl.py。默认按工作区布局查找，可用 PROJECTCTL 覆盖。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
F="$HERE/workdeck-fixture"
PC="${PROJECTCTL:-$HOME/ai/01.assets/project-control/projectctl.py}"

[ -f "$PC" ] || { echo "找不到 projectctl：$PC（用 PROJECTCTL 指定）" >&2; exit 1; }
[ -d "$F" ] || { echo "找不到夹具目录：$F" >&2; exit 1; }

cd "$F"
ctl() { python3 "$PC" "$@"; }

# 账本和状态投影每次重建，静态规格文件保持不变。
rm -rf 4.eval/control/events.jsonl 4.eval/control/ledger-head.json \
       4.eval/control/evidence 4.eval/control/reviews 4.eval/control/incidents \
       4.eval/loops PROJECT-STATUS.md
mkdir -p 4.eval/loops 4.eval/reviews

# 账本从零登记，之后所有状态都由 projectctl 自己写。
ctl init >/dev/null

loop() {  # loop <node> <行文>
  printf '# Loop — %s\n\n- attempt：%s\n- 结论：%s\n\n夹具日志，内容无意义。\n' \
    "$1" "${3:-1}" "$2" > "4.eval/loops/$1.md"
}
report() { # report <node> <结论>
  printf '# 评审报告 — %s\n\n结论：%s\n\n夹具评审，内容无意义。\n' "$1" "$2" \
    > "4.eval/reviews/$1-review.md"
}

# f01：走完整条验收路径，最终 accepted。它一 accepted，f06 就转 ready。
ctl start f01_collect_inputs --actor fixture-executor-a1 --role fixture_executor \
    --route-class economy --model fixture-model-economy >/dev/null
loop f01_collect_inputs "输入清单已落盘"
ctl submit f01_collect_inputs --actor fixture-executor-a1 \
    --summary "输入清单已落盘" --input-tokens 8000 --output-tokens 2000 \
    --elapsed-seconds 900 >/dev/null
report f01_collect_inputs accept
ctl review f01_collect_inputs --decision accept --reviewer fixture-reviewer-b1 \
    --role fixture_reviewer --route-class economy --model fixture-model-economy \
    --report 4.eval/reviews/f01_collect_inputs-review.md \
    --reason "交付物齐全且确定性检查通过" \
    --input-tokens 3000 --output-tokens 800 --elapsed-seconds 300 >/dev/null

# f02：第一次被打回，第二次还在跑。用来演示 running 且 attempt 为 2。
ctl start f02_build_core --actor fixture-executor-a1 --role fixture_executor \
    --route-class balanced --model fixture-model-balanced >/dev/null
loop f02_build_core "首轮核心产物缺少边界说明"
ctl submit f02_build_core --actor fixture-executor-a1 \
    --summary "核心产物初版" --input-tokens 40000 --output-tokens 12000 \
    --elapsed-seconds 4200 >/dev/null
report f02_build_core rework
ctl review f02_build_core --decision rework --reviewer fixture-reviewer-b1 \
    --role fixture_reviewer --route-class balanced --model fixture-model-balanced \
    --report 4.eval/reviews/f02_build_core-review.md \
    --reason "核心产物缺少边界说明，退回补充" --blocker "边界说明缺失" \
    --input-tokens 9000 --output-tokens 2500 --elapsed-seconds 600 >/dev/null
ctl start f02_build_core --actor fixture-executor-a1 --role fixture_executor \
    --route-class balanced --model fixture-model-balanced \
    --reason "按评审意见补充边界说明" >/dev/null
loop f02_build_core "正在补充边界说明" 2

# f03：提交后停住，等评审。
ctl start f03_write_report --actor fixture-executor-a2 --role fixture_executor \
    --route-class balanced --model fixture-model-balanced >/dev/null
loop f03_write_report "报告已提交待评审"
ctl submit f03_write_report --actor fixture-executor-a2 \
    --summary "夹具报告已提交" --input-tokens 25000 --output-tokens 9000 \
    --elapsed-seconds 3000 >/dev/null

# f04：卡在外部依赖，带明确的 owner 动作。
ctl block f04_external_probe --actor fixture-executor-a2 --role fixture_executor \
    --reason "外部接口没有可用凭据，夹具不允许读取任何凭据" \
    --required-action "owner 决定是否为该接口另开只读账号，或直接放弃本节点" >/dev/null

# f05：确认此路不通，走 failed 终态。
ctl start f05_legacy_path --actor fixture-executor-a2 --role fixture_executor \
    --route-class economy --model fixture-model-economy >/dev/null
loop f05_legacy_path "旧路径确认不可行"
ctl fail f05_legacy_path --actor fixture-executor-a2 --role fixture_executor \
    --reason "旧路径依赖的组件已下线，确认不可行" >/dev/null

# f06 依赖 f01，此刻应为 ready；f07 依赖 f06，应为 planned。两者都不动。
ctl --format json validate
echo
echo "夹具重建完成：$F"
