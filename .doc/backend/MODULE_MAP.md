# 后端模块地图

## 包级职责
| 包 | 职责 |
|---|---|
| `controller` | REST/SSE 协议入口、请求参数解析、响应封装 |
| `service` | 编排服务、流式适配、资源同步、历史读取、文件访问 |
| `agent` | Agent 定义加载、运行时实例、模式工厂 |
| `agent.mode` | ONESHOT/REACT/PLAN_EXECUTE 模式逻辑 |
| `agent.runtime` | 执行上下文、预算约束、工具执行 |
| `agent.runtime.policy` | `RunSpec`、`ToolChoice`、`Budget`、`ComputePolicy` |
| `model` | 模型注册、模型定义、协议枚举 |
| `model.api` | REST DTO 契约 |
| `model.stream`/`stream.*` | LLM delta 与 SSE 事件模型及组装 |
| `tool` | 内置工具 + 外置能力注册（backend/frontend/action） |
| `skill` | skill 目录解析、prompt 截断、注册刷新 |
| `memory` | 聊天窗口记忆与 JSONL 存储 |
| `security` | JWT 校验、JWKS、本地 key、chat image token |
| `config` | `@ConfigurationProperties` 与基础装配 |

## 依赖方向
- `controller -> service/registry`
- `service -> agent/tool/model/stream/memory/security`
- `agent.mode -> agent.runtime -> tool/service`
- `memory/security/config` 不依赖上层业务模块。

## 禁止跨层行为
- Controller 不写业务状态机。
- Mode 不直接访问 HTTP 层对象。
- Tool 不直接控制 SSE 协议细节。
