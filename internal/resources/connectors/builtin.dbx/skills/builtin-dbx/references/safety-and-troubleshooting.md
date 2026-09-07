# Safety And Troubleshooting

先使用 DBX 命令和错误 envelope 收集事实；只有信息不足时才定点检查非敏感配置字段。

## 安全边界

- `allow_actions` 控制连接允许的动作。
- `allow_tables` 控制可访问的表。
- SQL 动作必须与 `query`、`update`、`schema`、`admin` 命令匹配。
- 默认只允许单语句。
- 危险 update/delete 必须有足够收窄的条件。
- `tx` 只允许 `query` 和 `update` step。
- prod-like 连接不允许 `schema` 或 `admin`。

## 常见错误

### connection not found

```bash
dbx conn list
dbx conn show <name>
```

确认连接名；只有需要覆盖来源时才检查 `--config <path>`。

### action_blocked / action_mismatch

```bash
dbx conn show <name>
dbx inspect connection <name>
```

检查 `allow_actions`、`allow_tables`、SQL 类型与命令动作是否一致。

### missing_where

收窄 update/delete 条件，必要时增加 `--max-rows-affected`，先执行 `--dry-run`。

### multiple_statements_blocked

一次只提交一条 SQL；检查 SQL 文件是否包含多条语句。

### invalid_cursor

使用上页返回的 `next_cursor`，保持同一条 SQL 和相同的 `order by`。

### secret_store_unavailable / secret_key_not_found / encrypted_password_invalid

确认操作系统凭据库可用；在当前机器和用户下重新运行 `dbx secret encrypt`，更新连接中的加密 `password`。不要读取或输出密钥、明文密码或完整 DSN。

### SQLite 路径错误

```bash
dbx conn show <name>
dbx inspect connection <name>
```

确认解析后的路径；相对路径以连接配置文件目录为基准。

### tx 计划错误

确认 plan 包含 `steps`，每个 step 都有 `action` 和 `sql`，且 action 仅为 `query` 或 `update`。

## 排障顺序

1. 重跑失败的单条 DBX 命令并读取错误 envelope。
2. 用 `conn show/test` 和 `inspect connection/table` 验证连接与结构。
3. 不确定语法时查看目标命令的 `--help`。
4. 只有仍无法定位时，才检查非敏感配置字段或显式 `--config` 来源。
