# 记忆系统 Dialog 设计

## 总体设计

本次功能分成两个模块：

1. `偏好设置`
目标是把 `USER.md / AGENT.md / TEAM.md / GLOBAL.md` 这类 scope 视图做成可查看、可编辑、可保存的页面，但底层仍然回写 `SQLite`，再重新生成 md。

2. `记忆记录`
目标是把 SQLite 里的 memory 相关表完整透出，用户可以看到所有字段，而不只是 prompt 里可见的摘要。

当前系统约束不变：

- `SQLite` 是唯一事实源
- `snapshot/*.md` 是导出视图
- 保存后由后端刷新 md 快照

相关代码位置：

- 快照刷新：[snapshot_renderer.go](./../internal/memory/snapshot_renderer.go)
- 写入后自动刷新：[sqlite_store.go](./../internal/memory/sqlite_store.go)
- 表结构：[sqlite_store.go](./../internal/memory/sqlite_store.go)

## 模块 1：偏好设置

页面定位：面向 `fact + scope(user/agent/team/global)` 的管理页面。

## 原型图

```text
+------------------------------------------------------------------------------------------------+
| Memory Console / 偏好设置                                                     agent: go_runner |
+------------------------------------------------------------------------------------------------+
| Scope Tabs: [USER] [AGENT] [TEAM] [GLOBAL]                                                   刷新 |
+-------------------------------+--------------------------------------+-------------------------+
| Markdown View / Edit          | Scope Records                        | Detail Inspector        |
|-------------------------------|--------------------------------------|-------------------------|
| # USER                        | 标题             分类      重要度     | id: mem_101             |
|                               |--------------------------------------| scopeType: user         |
| - [mem_101] 偏好中文输出      | 偏好中文输出     general   8   编辑   | scopeKey: user:joe      |
|   category: general           | 默认先结论再解释 response  7   编辑   | status: active          |
|   importance: 8               | 时间统一北京时间 preference 9  编辑   | confidence: 0.95        |
|   confidence: 0.95            |                                      | tags: [preference]      |
|   tags: preference            | [新增偏好] [批量归档]               | createdAt: ...          |
|   content: 偏好中文输出       |                                      | updatedAt: ...          |
|                               |                                      | sourceType: tool-write  |
| [编辑] [预览] [结构化模式]    |                                      | raw fields JSON         |
| [保存] [取消]                 |                                      |                         |
+-------------------------------+--------------------------------------+-------------------------+
| 保存说明：修改的是 scope 视图，保存时回写 SQLite facts，再自动重建 USER.md                      |
+------------------------------------------------------------------------------------------------+
```

## 页面能力列表

- 查看某个 `scope` 当前的 markdown 视图
- 查看该 scope 下全部结构化 `fact`
- 在 markdown 模式和结构化模式之间切换
- 新增一条偏好
- 编辑现有偏好
- 删除一条偏好
- 批量归档偏好
- 保存后自动刷新 md 视图
- 展示保存前后 diff 摘要
- 展示字段级校验错误
- 支持只读查看 `AGENT.md / PROJECT.md` 的映射关系说明
- 支持按 `category / tag / importance` 过滤当前 scope 记录

## 模块 1 接口设计

### 1. 获取 scope 列表

`GET /api/memory/scopes?agentKey=go_runner`

用途：
返回当前 agent 下可编辑的 scope 摘要。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "agentKey": "go_runner",
    "scopes": [
      {
        "scopeType": "user",
        "scopeKey": "user:joe",
        "label": "USER",
        "fileName": "USER.md",
        "recordCount": 3,
        "updatedAt": 1777344000000
      },
      {
        "scopeType": "agent",
        "scopeKey": "agent:go_runner",
        "label": "AGENT",
        "fileName": "AGENT.md",
        "recordCount": 12,
        "updatedAt": 1777344100000
      },
      {
        "scopeType": "team",
        "scopeKey": "team:platform",
        "label": "TEAM",
        "fileName": "TEAM.md",
        "recordCount": 6,
        "updatedAt": 1777344200000
      },
      {
        "scopeType": "global",
        "scopeKey": "global:default",
        "label": "GLOBAL",
        "fileName": "GLOBAL.md",
        "recordCount": 4,
        "updatedAt": 1777344300000
      }
    ]
  }
}
```

### 2. 获取单个 scope 详情

`GET /api/memory/scope?agentKey=go_runner&scopeType=user`

用途：
返回当前 scope 的 markdown 视图和对应结构化记录。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "agentKey": "go_runner",
    "scopeType": "user",
    "scopeKey": "user:joe",
    "label": "USER",
    "fileName": "USER.md",
    "markdown": "# USER\n\n- [mem_101] 偏好中文输出\n  category: general\n  importance: 8\n  confidence: 0.95\n  tags: preference,language\n  content: 偏好中文输出，术语保持准确。\n",
    "records": [
      {
        "id": "mem_101",
        "title": "偏好中文输出",
        "summary": "偏好中文输出，术语保持准确。",
        "category": "general",
        "importance": 8,
        "confidence": 0.95,
        "status": "active",
        "scopeType": "user",
        "scopeKey": "user:joe",
        "tags": ["preference", "language"],
        "createdAt": 1777344000000,
        "updatedAt": 1777344300000
      }
    ],
    "meta": {
      "editable": true,
      "recordCount": 1,
      "generatedFromStore": true
    }
  }
}
```

### 3. 保存 scope 视图

`PUT /api/memory/scope`

用途：
保存当前 scope 的编辑结果。支持 `markdown` 和 `records` 两种模式。

#### 3.1 markdown 模式入参

```json
{
  "agentKey": "go_runner",
  "scopeType": "user",
  "scopeKey": "user:joe",
  "mode": "markdown",
  "markdown": "# USER\n\n- [mem_101] 偏好中文输出\n  category: general\n  importance: 8\n  confidence: 0.95\n  tags: preference,language\n  content: 偏好中文输出，术语保持准确。\n\n- [new] 默认先给结论再解释\n  category: response_style\n  importance: 7\n  confidence: 0.90\n  tags: style\n  content: 回答时先给结论，再展开解释。\n",
  "archiveMissing": true
}
```

#### 3.2 结构化模式入参

```json
{
  "agentKey": "go_runner",
  "scopeType": "user",
  "scopeKey": "user:joe",
  "mode": "records",
  "records": [
    {
      "id": "mem_101",
      "title": "偏好中文输出",
      "summary": "偏好中文输出，术语保持准确。",
      "category": "general",
      "importance": 8,
      "confidence": 0.95,
      "tags": ["preference", "language"]
    },
    {
      "id": "",
      "title": "默认先给结论再解释",
      "summary": "回答时先给结论，再展开解释。",
      "category": "response_style",
      "importance": 7,
      "confidence": 0.90,
      "tags": ["style"]
    }
  ],
  "archiveMissing": true
}
```

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "saved": true,
    "agentKey": "go_runner",
    "scopeType": "user",
    "scopeKey": "user:joe",
    "summary": {
      "created": 1,
      "updated": 1,
      "archived": 0,
      "unchanged": 0
    },
    "records": [
      {
        "id": "mem_101",
        "title": "偏好中文输出",
        "status": "active",
        "scopeType": "user",
        "scopeKey": "user:joe",
        "updatedAt": 1777345000000
      },
      {
        "id": "mem_202",
        "title": "默认先给结论再解释",
        "status": "active",
        "scopeType": "user",
        "scopeKey": "user:joe",
        "updatedAt": 1777345000000
      }
    ],
    "markdown": "# USER\n\n- [mem_101] 偏好中文输出\n  category: general\n  importance: 8\n  confidence: 0.95\n  tags: preference,language\n  content: 偏好中文输出，术语保持准确。\n\n- [mem_202] 默认先给结论再解释\n  category: response_style\n  importance: 7\n  confidence: 0.9\n  tags: style\n  content: 回答时先给结论，再展开解释。\n"
  }
}
```

### 4. 预检 markdown，不落库

`POST /api/memory/scope/validate`

用途：
用户点保存前校验 markdown 格式和字段合法性。

入参示例：

```json
{
  "agentKey": "go_runner",
  "scopeType": "user",
  "markdown": "# USER\n\n- [new] 偏好中文输出\n  importance: 99\n  content: xxx"
}
```

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "valid": false,
    "errors": [
      {
        "line": 4,
        "field": "importance",
        "message": "importance must be between 1 and 10"
      }
    ],
    "warnings": []
  }
}
```

## 模块 2：记忆记录

页面定位：memory 的完整观测台，偏运营、偏排查。

## 原型图

```text
+--------------------------------------------------------------------------------------------------+
| Memory Console / 记忆记录                                                                         |
+--------------------------------------------------------------------------------------------------+
| Filters: [kind v] [scopeType v] [status v] [category] [agentKey] [chatId] [keyword] [查询]      |
+--------------------------------------------------------------------------------------------------+
| Tabs: [All] [Facts] [Observations] [Links] [Snapshots]                                           |
+--------------------------------------------------------------------------------------------------+
| ID        Kind         Scope   Status    Category      Title              UpdatedAt      操作     |
|--------------------------------------------------------------------------------------------------|
| mem_101   fact         user    active    general       偏好中文输出       2026-04-28    查看     |
| mem_102   fact         team    active    workflow      周会固定周三       2026-04-28    查看     |
| mem_201   observation  chat    open      bugfix        修复权限问题       2026-04-27    查看     |
| ...                                                                                              |
+--------------------------------------------------------------------------------------------------+
| Drawer / Detail                                                                                  |
|--------------------------------------------------------------------------------------------------|
| 基础字段 | 原始 JSON | 关系 Links | Snapshot 归属 | Timeline | 操作按钮                         |
| id: mem_201                                                                                    |
| kind: observation                                                                              |
| scopeType: chat                                                                                |
| scopeKey: chat:chat-123                                                                        |
| status: open                                                                                   |
| sourceType: learn                                                                              |
| refId: run_abc                                                                                 |
| filesJson: []                                                                                  |
| toolsJson: ["_memory_search_"]                                                                 |
| confidence: 0.75                                                                               |
| ...                                                                                            |
+--------------------------------------------------------------------------------------------------+
```

## 页面能力列表

- 查看全部 memory 记录
- 按 `kind / scopeType / status / category / agentKey / chatId` 过滤
- 支持分页
- 支持全文关键词搜索
- 支持切换不同底层表视图
- 查看单条记录全部字段
- 查看原始 JSON
- 查看 links 关系
- 查看 snapshot 归属
- 查看 timeline
- 对 observation 执行 `promote`
- 对 fact 执行 `update / archive / contested`
- 导出筛选结果
- 查看 embedding 状态，不默认展开向量本体

## 模块 2 接口设计

### 5. 查询 memory 元信息

`GET /api/memory/meta`

用途：
返回前端筛选、编辑表单可直接使用的枚举值。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "categories": ["general", "preference", "constraint", "profile", "workflow", "decision", "glossary", "unresolved_issue", "bugfix", "todo", "project", "remember"],
    "types": ["fact", "observation"],
    "scopeTypes": ["user", "agent", "team", "chat", "global"],
    "statuses": ["active", "open", "superseded", "archived", "contested"],
    "sourceTypes": ["tool-write", "console-edit", "remember", "learn", "promote"]
  }
}
```

### 6. 预览当前问题会注入的 memory context

`POST /api/memory/context/preview`

用途：
输入当前对话框里的用户问题，返回如果发起真实 query 会注入上下文的 memory 文本、分层明细和选择决策。该接口用于排查 memory recall，不发起模型调用。

请求体：

```json
{
  "chatId": "chat-123",
  "message": "怎么发布 desktop builtin？"
}
```

约定：

- `chatId` 用于定位当前对话，并从 chat summary 推导 `agentKey` / `teamId`。
- `message` 使用和 `/api/query` 一致的字段名，作为 memory recall query。
- `topN`、`maxChars` 不由前端传入，固定读取后端 memory 配置。
- `userKey` 不由前端传入，来自鉴权 principal；本地无登录态时按默认用户处理。
- preview 复用真实 memory context 选择逻辑，但不创建新的 stable snapshot。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "message": "怎么发布 desktop builtin？",
    "agentKey": "go_runner",
    "chatId": "chat-123",
    "teamId": "team-1",
    "enabled": true,
    "summary": {
      "stableCount": 2,
      "sessionCount": 1,
      "observationCount": 3,
      "stableChars": 320,
      "sessionChars": 120,
      "observationChars": 580,
      "disclosedLayers": ["stable", "session", "observation"],
      "stopReason": "selected",
      "snapshotId": "snap_xxx",
      "candidateCounts": {"stable": 8, "session": 2, "observation": 5},
      "selectedCounts": {"stable": 2, "session": 1, "observation": 3}
    },
    "prompts": {
      "stable": "Runtime Context: Stable Memory\n- ...",
      "session": "Runtime Context: Session Memory\n- ...",
      "observation": "Runtime Context: Relevant Observations\n- ..."
    },
    "layers": [
      {
        "layer": "stable",
        "candidateCount": 8,
        "selectedCount": 2,
        "chars": 320,
        "items": [
          {
            "id": "mem_101",
            "kind": "fact",
            "scopeType": "agent",
            "scopeKey": "agent:go_runner",
            "title": "发布流程",
            "summary": "先 make release-program，再同步 desktop assets。",
            "category": "workflow",
            "importance": 9,
            "confidence": 0.95,
            "status": "active",
            "sourceType": "tool-write",
            "createdAt": 1777344000000,
            "updatedAt": 1777344200000,
            "order": 1
          }
        ]
      }
    ],
    "decisions": [
      {
        "layer": "stable",
        "reason": "scope_match",
        "itemIds": ["mem_101", "mem_102"]
      }
    ]
  }
}
```

### 7. 查询记录列表

`GET /api/memory/records?agentKey=go_runner&kind=fact&scopeType=user&status=active&limit=20&cursor=`

用途：
查询统一记录视图。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "count": 2,
    "nextCursor": "mem_102",
    "results": [
      {
        "id": "mem_101",
        "kind": "fact",
        "scopeType": "user",
        "scopeKey": "user:joe",
        "status": "active",
        "category": "general",
        "title": "偏好中文输出",
        "summary": "偏好中文输出，术语保持准确。",
        "importance": 8,
        "confidence": 0.95,
        "agentKey": "go_runner",
        "chatId": "chat-1",
        "sourceType": "tool-write",
        "updatedAt": 1777344300000
      },
      {
        "id": "mem_102",
        "kind": "fact",
        "scopeType": "user",
        "scopeKey": "user:joe",
        "status": "active",
        "category": "response_style",
        "title": "默认先给结论再解释",
        "summary": "回答时先给结论，再展开解释。",
        "importance": 7,
        "confidence": 0.90,
        "agentKey": "go_runner",
        "chatId": "chat-2",
        "sourceType": "remember",
        "updatedAt": 1777344400000
      }
    ]
  }
}
```

### 8. 获取单条记录详情

`GET /api/memory/record?agentKey=go_runner&id=mem_201`

用途：
返回完整字段和来源表信息。

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "id": "mem_201",
    "sourceTable": "MEMORY_OBSERVATIONS",
    "record": {
      "id": "mem_201",
      "requestId": "req_1",
      "chatId": "chat-123",
      "agentKey": "go_runner",
      "subjectKey": "chat-123",
      "kind": "observation",
      "refId": "run_abc",
      "scopeType": "chat",
      "scopeKey": "chat:chat-123",
      "title": "修复权限问题",
      "summary": "修复了查询接口的权限校验问题。",
      "sourceType": "learn",
      "category": "bugfix",
      "importance": 8,
      "confidence": 0.75,
      "status": "open",
      "tags": ["learned", "bugfix"],
      "createdAt": 1777344000000,
      "updatedAt": 1777344200000,
      "accessCount": 1,
      "lastAccessedAt": 1777344250000
    },
    "rawFields": {
      "runId": "run_abc",
      "detail": "修复了查询接口的权限校验问题。",
      "filesJson": [],
      "toolsJson": ["_memory_search_"]
    },
    "embedding": {
      "hasEmbedding": false,
      "model": ""
    }
  }
}
```

### 7. 查询关系 links

`GET /api/memory/links?agentKey=go_runner&id=mem_201`

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "id": "mem_201",
    "links": [
      {
        "id": "link_1",
        "fromId": "mem_201",
        "toId": "mem_101",
        "relationType": "supports",
        "weight": 1.0,
        "createdAt": 1777344300000
      }
    ]
  }
}
```

### 8. 查询 snapshots

`GET /api/memory/snapshots?agentKey=go_runner`

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "results": [
      {
        "id": "snap_abc123",
        "chatId": "chat-123",
        "agentKey": "go_runner",
        "stableItemIds": ["mem_101", "mem_102"],
        "observedItemIds": ["mem_201"],
        "createdAt": 1777344000000,
        "updatedAt": 1777344300000
      }
    ]
  }
}
```

### 9. 查询 timeline

`GET /api/memory/timeline?agentKey=go_runner&id=mem_201&limit=20`

响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "id": "mem_201",
    "count": 1,
    "results": [
      {
        "memory": {
          "id": "mem_101",
          "scopeType": "user",
          "title": "偏好中文输出",
          "content": "偏好中文输出，术语保持准确。"
        },
        "relationType": "supports",
        "direction": "outbound"
      }
    ]
  }
}
```

## 受控 Markdown 协议

为了支持“像文件一样编辑”，但不丢结构化字段，建议固定格式：

```md
# USER

- [mem_101] 偏好中文输出
  category: general
  importance: 8
  confidence: 0.95
  tags: preference,language
  content: 偏好中文输出，术语保持准确。

- [new] 默认先给结论再解释
  category: response_style
  importance: 7
  confidence: 0.90
  tags: style
  content: 回答时先给结论，再展开解释。
```

规则：

- `[mem_xxx]` 表示更新已有记录
- `[new]` 表示新增记录
- markdown 中缺失原有记录时，如果 `archiveMissing=true`，则归档
- 只允许编辑 `fact`
- 只允许 `user / agent / team / global`
- `chat` scope 不走这个编辑器

## 接口字段约束

偏好设置写接口建议校验：

- `agentKey` 必填
- `scopeType` 必填，只允许 `user/agent/team/global`
- `scopeKey` 可选，缺省按现有规则补全
- `mode` 必填，只允许 `markdown/records`
- `importance` 范围 `1-10`
- `confidence` 范围 `0-1`
- `status` 在偏好设置模块里固定写 `active`
- `kind` 在偏好设置模块里固定写 `fact`

记忆记录查询接口建议支持：

- `kind=fact|observation`
- `scopeType=user|agent|team|chat|global`
- `status=active|open|superseded|archived|contested`
- `category=<string>`
- `agentKey=<string>`
- `chatId=<string>`
- `keyword=<string>`
- `limit=<int>`
- `cursor=<string>`

## 原型能力清单汇总

偏好设置模块：

- scope 切换
- md 预览
- md 编辑
- 结构化编辑
- 保存前校验
- 保存后自动刷新
- 变更摘要
- 记录详情侧栏
- 单条新增/编辑/删除
- 批量归档
- 筛选与搜索

记忆记录模块：

- 多表统一列表
- 全字段详情
- JSON 原始视图
- 条件筛选
- 分页
- 关键词搜索
- timeline 查看
- links 查看
- snapshots 查看
- promote / update / archive 操作
- 导出结果
- embedding 状态查看

## 建议的第一阶段实现范围

为了尽快落地，第一阶段建议只做这些：

- `GET /api/memory/scopes`
- `GET /api/memory/scope`
- `PUT /api/memory/scope`
- `GET /api/memory/meta`
- `POST /api/memory/context/preview`
- `GET /api/memory/records`
- `GET /api/memory/record`

这样已经能覆盖：

- 偏好设置查看和编辑
- 记忆记录列表和详情
