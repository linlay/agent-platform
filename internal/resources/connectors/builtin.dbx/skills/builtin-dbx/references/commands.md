# Commands

## 直接调用

在 Agent Platform 中直接执行任务相关的 `dbx` 命令。不要探测 builtin 路径或配置目录。只有不确定参数时才运行对应的 `dbx <command> --help`。

## 连接与结构

```bash
dbx conn list
dbx conn show <name>
dbx conn test <name>
dbx inspect connection <name>
dbx inspect schema <name> [schema]
dbx inspect table <name> <table> [schema]
```

先确认连接和表结构，再执行 SQL。

## SQL 动作

```bash
dbx query <conn> '<read-only-sql>'
dbx update <conn> '<dml-sql>'
dbx schema <conn> '<ddl-sql>'
dbx admin <conn> '<admin-sql>'
```

- `query`：只读 SQL。
- `update`：insert、update、delete、merge。
- `schema`：create、alter、drop、truncate、rename。
- `admin`：grant、revoke、set、vacuum、analyze 等管理 SQL。

也可从文件读取单条 SQL：

```bash
dbx query file <conn> ./query.sql
dbx update file <conn> ./change.sql
```

常用参数：

- `--format json|table`
- `--page-size <n>` / `--cursor <n>`
- `--dry-run`
- `--max-rows-affected <n>`
- `--verbose`
- `--config <path>`：只读取指定文件或目录。

## 导入与导出

```bash
dbx import file ./rows.csv <conn> <table> --dry-run
dbx import file ./rows.csv <conn> <table>
dbx export table <table> <conn> ./rows.csv --format csv
dbx export table <table> <conn> ./rows.json --format json --limit 500
```

导入使用 `update` 权限；导出使用 `query` 权限。导入前检查表结构，导出后验证文件。

## 事务

```json
{
  "steps": [
    {"action": "query", "sql": "select id from users where id = 1"},
    {"action": "update", "sql": "update users set active = 1 where id = 1", "max_rows_affected": 1}
  ]
}
```

```bash
dbx tx <conn> --plan ./plan.json --dry-run
dbx tx <conn> --plan ./plan.json
```

`tx` 只接受 `query` 和 `update` step；任一步失败时整笔事务回滚。
