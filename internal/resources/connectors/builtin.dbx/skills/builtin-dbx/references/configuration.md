# Configuration

只在创建连接、覆盖配置来源或排查配置错误时阅读本文件。正常数据库操作直接使用 DBX 命令。

## 配置位置

在 Agent Platform 中，DBX 先读取当前 agent 的 `$AP_AGENT_CONFIG_HOME/dbx`，再读取 `~/.config/dbx`；同名连接以 agent 私有配置为准。正常操作不要展开变量、列目录或读取 raw config。

每个连接对应一个 `<name>.toml`：

```toml
[connection]
engine = "postgres"
host = "127.0.0.1"
port = 5432
user = "app"
database = "appdb"
password = "dbx-aes-gcm:v2:..."
sslmode = "disable"
allow_actions = ["query"]
allow_tables = ["users", "public.audit_*"]
tags = ["dev"]
```

`--config <path>` 可以指定单个 TOML 文件或配置目录；使用后只读取该来源。

## Engine

支持：

- `postgres` / `postgresql`
- `mysql`
- `sqlite` / `sqlite3`

SQLite 示例：

```toml
[connection]
engine = "sqlite"
path = "./demo.db"
allow_actions = ["query"]
allow_tables = ["users"]
```

SQLite 相对 `path` 以配置文件目录为基准。

## 当前字段

- 连接：`engine`、`dsn`、`dsn_env`、`host`、`port`、`user`、`password`、`database`、`schema`、`path`、`sslmode`
- 行为：`readonly`、`timeout`、`role`、`tags`
- 权限：`allow_actions`、`allow_tables`

`allow_actions` 必填，可选值为 `query`、`update`、`schema`、`admin`。`allow_tables` 可省略；配置后支持 `*` 通配符。

## 密码

运行：

```bash
dbx secret encrypt
```

把输出的 `dbx-aes-gcm:v2:...` 写入 `password`。密钥保存在当前操作系统用户的凭据库中，密文绑定当前机器和用户。不得输出、解密或提交密码与密文。

`dsn_env` 可用于从环境变量读取 DSN；若 DSN 内含密码，优先改用结构化连接字段和加密 `password`。
