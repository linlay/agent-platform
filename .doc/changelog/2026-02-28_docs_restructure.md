# 2026-02-28 文档结构重排（指引 + 接口规范 + request/chat/meta）

## 变更类型
- 文档体系重构（不改运行时代码）。

## 本次重排
1. 合并旧入口与术语文档，形成统一入口：`.doc/GUIDE.md`（标题：指引）。
2. 合并旧通用 API 约定与错误码说明，形成：`.doc/api/SPEC.md`（标题：接口规范（含错误码））。
3. API 模块重组为：
   - `api/modules/request.md`（query/submit/upload规划）
   - `api/modules/chat.md`（chat/chats/read/data/viewport）
   - `api/modules/meta.md`（agent/skill/tool）
4. 下线历史前端集成目录，相关协议内容并入 `request/chat/DATA_FLOW`。
5. `DATA_FLOW.md` 补充：
   - 多工具调用（单轮多 `tool_call_id`）
   - frontend tool 回填链路
   - `action.end/tool.end` 作为 `args` 结束边界语义
6. 统一 submit 事件主名为 `request.submit`（语义：tool 参数回填）。

## 契约澄清
- 当前版本不新增 upload API，仅在 request 文档中保留“规划中”占位。
- 文档命名采用英文文件名，中文标题。

## 清理项
- 清理被合并的旧入口、旧 API 分拆模块与旧前端集成文档。
- 删除历史前端目录，仅保留后端协议与数据流事实源。
