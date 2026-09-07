---
name: builtin-httpx
description: "Use this skill whenever another skill or user needs to discover, inspect, run, or troubleshoot configured httpx site/action workflows, including fixed site/action workflows supplied by domain skills. Use JSON-file inputs for user-provided text, paths, structured values, quotes, newlines, or other shell-sensitive content on every OS, especially Windows PowerShell 5.1. Treat httpx as a stateful site/action CLI with explicit action contracts, not as curl and not as a raw config/state file interface."
metadata:
  version: "0.2.0"
---

# httpx

把 `httpx` 当作面向 site/action 的状态化 HTTP CLI。先读取 action 契约，再检查编译结果，最后执行请求；不要猜 URL、header、cookie、body 或 raw state 结构。

若领域 skill 已固定 site/action 并附带契约，则由领域 skill 决定业务流程和参数语义；仍必须应用本 skill 的 Shell 路由与参数传输规则。只有领域流程明确要求或发生配置、action、契约异常时，才定点使用通用 discovery。

## Shell 与输入路由

- 本版要求 `httpx >= v0.1.8`。若 `httpx action <site> <action>` 未显示 `--param-json-file`，停止并报告 builtin 版本过旧；不得回退内联 JSON 或包装器。
- 实际 Shell 以 Platform 的运行环境说明为准；Windows Host 可使用随包 Git Bash，关闭该能力时使用配置的 Shell。仅在实际为 Windows PowerShell 5.1 时应用对应引用文档，不要自行切换 Shell 或套多层 Shell 试错。
- 用户提供的文本、标题、路径以及任何 JSON、数组、对象、引号、换行或 shell-sensitive 值，一律由 `file_write` 写入严格 UTF-8 JSON object，再通过 `--param-json-file <absolute-path>` 传入。
- `--extract` 的结构化输入一律写入独立 JSON object，并通过 `--extract-json-file <absolute-path>` 传入。
- 只有 agent 自己生成且不含空格或 shell 特殊字符的短标量，例如稳定的 request ID，才允许直接使用 `--param key=value`。
- 禁止使用 stdin、管道、重定向、here-string、命令替换、临时 Python/CMD/PS1 包装器或 Shell 变量生成 JSON。Windows PowerShell 5.1 必须追加读取 [Windows PowerShell 5.1 调用](references/windows-powershell-5.1.md)。
- 每次 `bash` 工具调用 只运行一个 `httpx` 命令；禁止 `&&`、`||`、`;`、管道、输出重定向以及用 `head`、`tail`、`sed` 截断或吞掉 JSON。

## 推荐流程

site 未知时先发现：

```bash
httpx sites
```

site 已知时按需执行：

```bash
httpx site <site>
httpx actions <site>
httpx action <site> <action>
httpx inspect <site> <action> ...
httpx run <site> <action> ...
```

- 始终先读 `action <site> <action>` 给出的 Usage、Flags、Params、Extracts 和 Examples，不要猜输入。
- 对写入、删除、流转或提交表单等有副作用的 action，先用完全相同的参数执行 `inspect`。
- 仅在 `site <site>` 显示内建 login 且确实需要刷新登录态时执行 `httpx login <site>`。OIDC/SSO 等复杂登录使用外部流程。
- 仅在认证或状态异常时执行 `httpx state <site>`；该命令只显示摘要，不显示保存值。

`inspect` 只编译请求，不发送请求。普通 `inspect` 会脱敏，不能证明 `env`、`file`、`secret` 或 `state` 来源当前可读。只有诊断确实需要时才使用 `inspect --reveal`；其输出可能包含敏感值。

## 输入与执行

- `--param key=value` 参与请求编译，可重复；包含空格或 shell 特殊字符时引用整个 `key=value`。
- `--param-json-file <path>` 从一个 JSON object 读取 typed 参数；object、array、number、boolean 和 `null` 保持类型。单个文件上限 1 MiB。
- `--extract-json-file <path>` 从一个 JSON object 读取 typed extractor 输入，只参与 extractor 执行；单个文件上限 1 MiB。
- 文件与内联参数同时存在时，先读文件，再由 `--param` 或 `--extract` 覆盖同名顶层字段；不递归合并。
- 不使用 `-` 读取 stdin。参数文件必须位于当前 Chat 或临时可写目录，并使用 `file_write` 返回的真实绝对路径。
- `--format json` 输出结构化结果。
- 未传 `--config` 时，先查 `$AP_AGENT_CONFIG_HOME/httpx`，再回退 `~/.config/httpx`。显式 `--config <dir>` 只读取该目录。

把参数文件视为请求的一部分：inspect 与后续完全相同的 run 复用同一文件；批处理 inspect、validate、execute 复用同一文件。修改 action、参数或 timeout 后创建新文件；是否更换 `request_id` 仍以领域 skill 的幂等规则为准。

动态值来源为：`literal`、`param`、`env`、`file`、`secret`、`shell`、`state`。

- `from = "env"` 只读取配置中 `key` 指定的精确环境变量名；变量不存在时失败。
- `from = "secret"` 直接读取对应 scope 下 `<site>.json` 的指定 key，不需要预加载步骤。
- `from = "state"` 读取对应 scope 下该 site 保存的值。

## Storage 与 Scope

| 配置 | 控制范围 |
| --- | --- |
| `from = "secret", scope = ...` | 单个动态 secret 来源 |
| `from = "state", scope = ...` | 单个动态 state 来源 |
| site 顶层 `state_scope` | cookie、`save`、`last_login` 的存储目标 |
| `[login].secret_scope` | 内建登录的 username/password 来源 |

`scope` 只接受 `global` 和 `chat`，省略时固定为 `global`。

Global 目录：

- secret：`$XDG_SECRET_HOME/httpx` → `$XDG_DATA_HOME/secret/httpx` → `~/.local/secret/httpx`
- state：`$XDG_STATE_HOME/httpx`，未设置时使用 `~/.local/state/httpx`

Chat 目录：

- secret：`$AP_CHAT_DIR/.secret/httpx`
- state：`$AP_CHAT_DIR/.state/httpx`

Chat scope 要求平台注入的 `AP_CHAT_DIR` 是已存在、可访问的绝对非根目录；缺失或无效时直接失败，不回退 global。不要自行猜测或覆盖该变量。

`--state <dir>` 只覆盖 global state 目录，不影响 Chat state。`site <site>` 和 `state <site>` 根据配置的 `state_scope` 展示对应摘要。

## 最小排障

- 配置或 action 错误：依次检查 `sites`、`site`、`actions`、`action`，再用 `inspect`；仅在内置信息不足或正在维护配置时定点读取 TOML。
- `401` / `403` 或 state 错误：检查 `site` 和 `state` 摘要、`state_scope` 与 `AP_CHAT_DIR`；仅在支持内建登录时执行 `login`。不要用 `--state` 尝试覆盖 Chat state。
- 动态输入错误：检查必填参数文件、typed 字段，以及报错指出的精确 `env`、`file`、`secret`、`shell` 或 `state` 来源。
- `assertion_error`：检查 `expect_status`、extractor 和 `save`，再确认编译后的请求是否正确。
- `invalid --param-json-file`、顶层非 object 或超过 1 MiB：修正文件或拆分业务批次，不得回退内联 JSON。
- Windows 出现 `ParserError`、`CommandNotFoundException` 或 JSON 双引号丢失时：说明 JSON 仍进入了 Shell；停止执行并按 [Windows PowerShell 5.1 调用](references/windows-powershell-5.1.md) 改为直接文件输入。

## 安全约束

- 默认使用 discovery 和 state 摘要，不读取 raw secret/state JSON。
- 不展示 token、cookie、password、完整认证 header 或 `inspect --reveal` 的敏感结果。
- state 与 secret 都可能明文保存凭证，不要提交到仓库。
- 参数文件只写入当前 Chat 或临时可写目录；不得写入 Skill 目录，也不得把包含敏感参数的文件提交到仓库。结果已确定且不再需要原样重试后再清理。
