# Meta 模块 API（agent / skill / tool）

## 统一说明
- 本模块聚合元数据查询接口，不承载执行态流式过程。
- 无全局分页参数；如需过滤，仅使用各接口定义的 `tag/kind`。

## 1. Agent 接口
### `GET /api/ap/agents`
返回 Agent 摘要列表（支持 `tag` 模糊过滤）。

Query 参数：
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `tag` | string | 否 | 匹配 `id/description/role/tools/skills` |

成功 `data=AgentSummary[]`：
- `key`, `name`, `icon`, `description`, `role`, `meta`
- `meta` 常见字段：`model`, `mode`, `tools[]`, `skills[]`

### `GET /api/ap/agent?agentKey=...`
按 key 返回 Agent 详情。

成功 `data=AgentDetail`：
- `key`, `name`, `icon`, `description`, `role`, `instructions`, `meta`

失败场景：
| 场景 | HTTP | code |
|---|---|---|
| `agentKey` 不存在 | 400 | 400 |

## 2. Skill 接口
### `GET /api/ap/skills`
查询 skill 摘要列表。

Query 参数：
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `tag` | string | 否 | 匹配 `id/name/description/prompt` |

成功 `data=SkillSummary[]`：
- `key`, `name`, `description`, `meta.promptTruncated`

### `GET /api/ap/skill?skillId=...`
查询 skill 详情。

成功 `data=SkillDetail`：
- `key`, `name`, `description`, `instructions`, `meta.promptTruncated`

失败场景：
| 场景 | HTTP | code |
|---|---|---|
| `skillId` 为空 | 400 | 400 |
| 未找到 skill | 400 | 400 |

## 3. Tool 接口
### `GET /api/ap/tools`
查询工具列表，支持 `kind/tag` 过滤。

Query 参数：
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `tag` | string | 否 | 匹配名称、描述、hint、toolType 等 |
| `kind` | string | 否 | `backend` / `frontend` / `action` |

成功 `data=ToolSummary[]`：
- `key`, `name`, `description`
- `meta`: `kind`, `toolType`, `toolApi`, `viewportKey`, `strict`

失败场景：
| 场景 | HTTP | code |
|---|---|---|
| `kind` 非法 | 400 | 400 |

### `GET /api/ap/tool?toolName=...`
查询工具详情。

成功 `data=ToolDetail`：
- `key`, `name`, `description`, `afterCallHint`, `parameters`, `meta`

失败场景：
| 场景 | HTTP | code |
|---|---|---|
| `toolName` 为空 | 400 | 400 |
| 工具不存在 | 400 | 400 |
