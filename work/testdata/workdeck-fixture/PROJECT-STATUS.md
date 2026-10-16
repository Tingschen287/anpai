<!-- GENERATED FILE — DO NOT EDIT. -->

# workdeck-fixture — 当前状态

- 生成时间：2026-09-20T02:32:20+00:00
- 项目生命周期：poc
- Graph revision：3
- 事件游标：evt_000014_bafc4d1a
- 总体健康度：blocked

## 节点摘要

| status | count |
|---|---:|
| planned | 1 |
| ready | 1 |
| running | 1 |
| submitted | 1 |
| accepted | 1 |
| rework | 0 |
| blocked | 1 |
| failed | 1 |
| superseded | 0 |

## 下一可执行节点

| id | title | route_class | owner_role | dependencies |
|---|---|---|---|---|
| f06_owner_gate | Owner 裁决门：等待人来拍板 | human | human_owner | f01_collect_inputs |

## 执行中与待评审

| id | status | actor | attempt | latest evidence |
|---|---|---|---:|---|
| f02_build_core | running | fixture-executor-a1 | 2 | none |
| f03_write_report | submitted | fixture-executor-a2 | 1 | 4.eval/control/evidence/f03_write_report/attempt-001/submission-c09ecc9e9b014d17b602c85d43c07d11/manifest.json |

## 阻塞与失败

| id | status | reason | required action |
|---|---|---|---|
| f04_external_probe | blocked | 外部接口没有可用凭据，夹具不允许读取任何凭据 | owner 决定是否为该接口另开只读账号，或直接放弃本节点 |
| f05_legacy_path | failed | 旧路径依赖的组件已下线，确认不可行 |  |

## 预算

| worker_rounds | frontier_calls | tokens | cost_cny |
|---:|---:|---:|---:|
| 5/20 | 0/8 | 111300/1200000 | 0.0/not_enforced |

## 最近事件

| occurred_at | event_id | node | event | actor | evidence |
|---|---|---|---|---|---|
| 2026-09-20T02:32:19+00:00 | evt_000005_49862c7e | f01_collect_inputs | node_accepted | fixture-reviewer-b1 | 4.eval/control/reviews/f01_collect_inputs/attempt-001/review-743cfe7468744a198f4fe74751ab9832/snapshots/report/f01_collect_inputs-review.md, 4.eval/control/reviews/f01_collect_inputs/attempt-001/review-743cfe7468744a198f4fe74751ab9832/review.json |
| 2026-09-20T02:32:19+00:00 | evt_000006_84a212bc | f02_build_core | node_started | fixture-executor-a1 | none |
| 2026-09-20T02:32:19+00:00 | evt_000007_44386a62 | f02_build_core | node_submitted | fixture-executor-a1 | 4.eval/control/evidence/f02_build_core/attempt-001/submission-eecb2ac7c71b4cabbe0792d626cf4b7c/manifest.json |
| 2026-09-20T02:32:20+00:00 | evt_000008_068de559 | f02_build_core | node_rework | fixture-reviewer-b1 | 4.eval/control/reviews/f02_build_core/attempt-001/review-404cb830258a4f2d9d4c165eac7bcb24/snapshots/report/f02_build_core-review.md, 4.eval/control/reviews/f02_build_core/attempt-001/review-404cb830258a4f2d9d4c165eac7bcb24/review.json |
| 2026-09-20T02:32:20+00:00 | evt_000009_93ce7b66 | f02_build_core | node_started | fixture-executor-a1 | none |
| 2026-09-20T02:32:20+00:00 | evt_000010_7f9a5eeb | f03_write_report | node_started | fixture-executor-a2 | none |
| 2026-09-20T02:32:20+00:00 | evt_000011_f5d2b453 | f03_write_report | node_submitted | fixture-executor-a2 | 4.eval/control/evidence/f03_write_report/attempt-001/submission-c09ecc9e9b014d17b602c85d43c07d11/manifest.json |
| 2026-09-20T02:32:20+00:00 | evt_000012_317733d6 | f04_external_probe | node_blocked | fixture-executor-a2 | none |
| 2026-09-20T02:32:20+00:00 | evt_000013_e3b0a8fd | f05_legacy_path | node_started | fixture-executor-a2 | none |
| 2026-09-20T02:32:20+00:00 | evt_000014_bafc4d1a | f05_legacy_path | node_failed | fixture-executor-a2 | none |
