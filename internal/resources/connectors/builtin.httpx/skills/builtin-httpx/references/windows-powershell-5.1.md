# Windows PowerShell 5.1 调用

本页仅适用于 Platform 运行环境明确使用 Windows PowerShell 5.1 的情况。复杂值不进入 PowerShell 参数；只把由 `file_write` 返回的绝对文件路径交给 `httpx.exe`。

## 固定流程

1. 确认 `httpx action <site> <action>` 显示 `--param-json-file`；缺失时报告需要升级到 `httpx >= v0.1.8`。
2. 使用 `file_write` 在当前 Chat 或临时可写目录创建严格 UTF-8 JSON object。不要使用 `Set-Content`、`Out-File`、重定向、here-string、Shell 变量或临时脚本生成 JSON。
3. 使用文件的真实绝对路径直接执行：

   ```powershell
   httpx.exe inspect <site> <action> --param-json-file '<absolute-params-file>'
   httpx.exe --format json run <site> <action> --param-json-file '<absolute-params-file>'
   ```

4. extractor 输入写入另一个 JSON object，并使用 `--extract-json-file '<absolute-extract-file>'`。
5. 通过 Bash 工具的 `cwd` 字段选择工作目录，不在命令里执行 `cd`。
6. 结果未知且请求完全相同时复用原文件；修改 action、参数或 timeout 后创建新文件，并按领域 skill 规则处理 request ID。

PowerShell 单引号只保护文件路径，不承载业务 JSON。不得调用已删除的 `invoke-httpx.ps1`，也不得改用 `cmd /c`、Bash、管道、stdin、命令替换或更多反斜杠试错。

## 文件约束与失败处理

- 参数文件和 extractor 文件顶层都必须是 JSON object，单文件不超过 1 MiB。
- `read --param-json-file` / `read --extract-json-file`：检查绝对路径、文件存在性和读取权限。
- `invalid --*-json-file`：用 `file_read` 检查严格 JSON、顶层 object 和 UTF-8 内容，不要让 PowerShell 重新序列化。
- `input exceeds 1 MiB`：按领域边界拆分业务批次，不回退内联参数。
- `unknown flag: --param-json-file`：当前 `httpx.exe` 过旧；停止并升级，不启用兼容包装器。
- HTTPX 自身非零退出时按领域 skill 的恢复和幂等规则处理。
