# 模型路由与升级策略

Graph 只引用稳定的 `route_class`；具体映射在本文件维护。

## 当前映射

| route_class | 适用工作 | executor | reviewer |
|---|---|---|---|
| `economy` | 固定格式检查 | 本地确定性脚本 | 独立脚本 |
| `balanced` | 有界调研与常规验证 | 夹具执行者 | 不同会话 |
| `frontier` | 架构与最终评审 | 夹具执行者 | 独立会话 |
| `human` | 预算与方向裁决 | 夹具 owner | 夹具 owner |

本文件属于夹具，映射关系是假的，不用于任何真实派工。
