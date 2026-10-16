# 1.design — 决策日志

项目状态由 Graph 和事件账本推导；本文只记录方案选择及其依据。

## 决策清单

### adr_f01 — 夹具覆盖全部展示状态

- 日期：2026-09-20；关联节点 f01 至 f07；Graph revision 3。
- 状态：accepted。
- 决策主体：夹具重建脚本。

夹具刻意让七个节点分别停在 accepted、running、submitted、blocked、failed、ready
和 planned，使读取方在一个账本上就能看到全部状态分支，不必造多个夹具。
