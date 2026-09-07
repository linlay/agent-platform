# Output And Behavior

## Output Formats

SQL、`conn`、`inspect`、`import`、`tx` 等命令默认使用结构化输出。

常见格式：

- `json`：默认，适合脚本和智能体
- `table`：适合人类直接看查询结果

`export` 自己写文件，不走 `table` / `json` 查询展示格式，而是通过 `--format csv|json` 控制输出文件格式。

## Query Pagination

`query` 默认最多物化 `100` 行。

```bash
dbx query local-sqlite 'select * from users order by id' --page-size 100
dbx query local-sqlite 'select * from users order by id' --cursor 100
```

要点：

- 返回体里会带当前 `cursor` 和可能存在的 `next_cursor`
- 继续读取时要沿用同一条 SQL
- 最稳妥的做法是固定 `order by`

## Verbose Metadata

`--verbose` 会让输出附带更多连接元信息，常见包括：

- engine
- 连接目标摘要
- `allow_actions`
- tags
- 环境推断结果

适合：

- 核对当前连接到底是不是你以为的那个目标
- 排查为什么某个动作被 policy 拦住

## Dry Run

`--dry-run` 只验证 policy 和输入，不执行真实写操作。

适用命令：

- `query` / `update` / `schema` / `admin`
- `import`
- `tx`

典型行为：

- SQL 命令返回 policy 校验通过的计划信息
- `import` 返回校验过的列和行数
- `tx` 返回已验证的 steps

`export` 没有 `--dry-run`。

## Policy Behavior

`dbx` 会先做自己的分类和策略校验，再决定是否执行。

重点行为：

- SQL 先被分类成 read / write / ddl / admin
- 命令动作必须和 SQL 分类一致
- `allow_actions` 必须放行当前动作
- `update` / `delete` 缺少 `WHERE` 会被拦截
- 多语句默认阻断

## Prod-Like Connections

连接名或 tags 含有 `prod` 时，会被当成 prod-like。

当前规则：

- prod-like 连接如果配置了 `schema` 或 `admin`，会在连接解析阶段直接报错
- 这层保护发生在 DBX 侧，不依赖数据库账号本身是否有权限

## Practical Reading Tips

读结果时优先看：

1. `kind`
2. `summary`
3. `action`
4. `data`
5. `meta` 或 `verbose` 元信息

如果要排障，再看错误 code 是否属于：

- `action_blocked`
- `action_mismatch`
- `multiple_statements_blocked`
- `missing_where`
- `invalid_cursor`
