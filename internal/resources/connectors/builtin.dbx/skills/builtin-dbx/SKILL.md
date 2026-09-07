---
name: builtin-dbx
description: "Use this skill to operate the Agent Platform dbx builtin for MySQL, PostgreSQL, or SQLite: discover connection and schema details, run query/update/schema/admin/import/export/tx commands, and diagnose current DBX policy or connection errors."
version: 0.1.0
---

# dbx

先读这个 skill，再操作 `dbx`。

## 调用规则

- `dbx` 是 Agent Platform 已注入 `PATH` 的 builtin，可直接执行。
- 直接运行任务所需的 `dbx ...`。不要先执行 `which dbx`、`command -v dbx`、`ls`、PATH 输出、目录扫描或等价探测。
- 每次 Bash 调用优先只执行一条 DBX 命令，不在前后拼接 `ls`、`which`、`echo`、重定向或 `&&`。
- Platform 会自动注入 `AP_AGENT_CONFIG_HOME`；DBX 会优先读取当前 agent 的私有 `dbx/` 配置。正常调用不要展开、打印或检查该变量。
- 只有用户明确要求覆盖配置来源，或 DBX 已返回配置定位错误时，才使用 `--config <path>`。
- 只有直接调用返回 command-not-found 时，才报告 Platform builtin 分发或启动问题。

## 工作流

1. 根据任务直接运行 `dbx conn list`、`dbx conn show <name>` 或 `dbx conn test <name>`。
2. 用 `dbx inspect connection/schema/table` 获取真实连接与结构信息。
3. 选择匹配的动作：只读用 `query`，DML 用 `update`，DDL 用 `schema`，管理 SQL 用 `admin`。
4. 写入、导入或事务先缩小范围，并使用可用的 `--dry-run`、`--max-rows-affected` 或事务约束。
5. 执行后根据 envelope、影响行数或后续只读查询复核。

已知命令契约时直接执行。只有不确定语法时才查看对应的 `dbx <command> --help`；不要机械执行整套 help。

## 安全边界

- 先 `conn` / `inspect`，再执行 SQL、导入导出或事务。
- 遵守连接的 `allow_actions` 与 `allow_tables`；不得绕过动作分类。
- 默认单语句；危险写入必须有足够收窄的条件。
- `tx` 只包含 `query` / `update` step。
- 不读取或展示密码、密文、DSN 凭据、系统凭据库内容或 raw config。
- 正常使用不读取 agent 私有配置目录、`~/.config/dbx/*.toml` 或 SQLite 数据库文件。

## References

- 命令、参数和示例：`references/commands.md`
- 配置结构和加密密码：`references/configuration.md`
- 安全错误与排障：`references/safety-and-troubleshooting.md`
- 输出、分页和 dry-run：`references/output-and-behavior.md`
