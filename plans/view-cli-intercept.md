# VIEW 拦截连接器 CLI

## 一句话

连接器在 `view.json` 里把 CLI 写操作声明为 action（结构化参数模板 + `paramsSchema`）。模板能“由参数拼命令”，也能“由命令反解参数”：用户在 display 页面点按钮走前者；AI 在 Bash 里直接调用该命令时走后者，平台把反解出的参数放进同一 VIEW 的 form 让用户审阅，提交后按模板重建命令执行，不经模型。`.bash-hooks` 整体删除。

## 进度（2026-10-27）

- Platform 已实现：`intercept`/`json` 声明与校验、`view.CLIArgs` 与反向 `ParseCLIArgs`、Bash 审查识别单条连接器调用（Host 与 Container 走同一审查代码，测试只覆盖 Host；`bash -c` 内的连接器计入 ConnectorIDs）、`checkBashHITL` 改为 VIEW 拦截、提交按模板重建、`.bash-hooks` 全部删除（含 approval 模式与 `--payload` 改写）、文档更新。`go test ./...` 通过（`internal/hostenv` 的 npm 发现测试在满载时偶发超时，单独运行通过，与本改动无依赖）。
- 实现时补充的规则：缺少 required 参数的调用照常打开表单由用户补齐，提交按完整 schema 校验；多个拦截路径同时命中取最长，等长拒绝；拦截表单不可由 Mailbox 上级代批。
- Jira 连接器（zenmind-env，未提交）：`worklog` 增加 `form` 用途，`worklog.add` 标 `intercept`；`worklog.js` 增加 form 分支（单条审阅、collect 校验、`frontend_awaiting_invalid`、`awaiting_resize`），新增 `qa/worklog-form.cjs`，与原 `qa/view-actions.cjs` 均通过；SKILL 与 mutations 参考改为直接调用、不加 `--dry-run`。
- mock 技能已删除 `.bash-hooks`。
- WebClient 已核对：连接器 form VIEW 不注入 viewHost，按原始 awaiting 协议通信，现有实现支持，无需改动。

未完成：

- `issue worklog add-batch --payload`：本机没有 Jira CLI 源码，只有构建产物，批量命令待 CLI 实现后再加 `worklog.batch` 拦截 action；目前多条工时一条一次确认，或使用 display VIEW 一次填写。
- 跨天 RFC3339 写法不符合 `worklog.add` 的 schema，经拦截会被拒绝；需要时扩展 action 或按日拆分。
- 未在真实 Jira 与运行中的 Platform/WebClient 上端到端验证。

## 已确认

- VIEW 两种主要用法：display（` ```view ` 块）与 form（拦截审阅）。`ask_user_form` + view 本期不做。
- 拦截的单位是 action；只有标了 `intercept: true` 的 action 会拦截，其余命令照常执行。
- 只能拦截已挂载连接器自己的 CLI，不能拦截 `rm`、`git` 等通用命令；通用风险仍归平台 Shell 分析与 AccessPolicy。
- 批量数据用 `json: true` 作为一个参数传递，不走 stdin（stdin 需要重定向，就不是直接调用）。
- 删除 `.bash-hooks`（含 approval 与 form 两种模式、`--payload` 改写）。只做技能、未打包成连接器的 CLI 不再能声明业务确认。
- 拦截表单始终人工提交，不受 `auto_approve` / `full_access` 影响；命中后不再叠加普通 Bash 审批。

## 声明

```json
"worklog": {
  "usage": ["display", "form"],
  "actions": {
    "worklog.batch": {
      "description": "批量登记工时",
      "adapter": "cli",
      "intercept": true,
      "args": ["--no-input", "issue", "worklog", "add-batch",
               {"opt": "--payload", "param": "payload", "json": true}, "--json"],
      "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["payload"],
        "properties": {"payload": {"type": "object"}}}
    }
  }
}
```

- `intercept`：仅 cli；所在 VIEW 必须声明 `form` 用途；不能与 `stdinParam` 同用；模板必须含至少一个不以 `-` 开头的字面量。
- 子命令路径 = 模板中不以 `-` 开头的字面量（上例 `issue worklog add-batch`）。同一连接器内，被拦截 action 的子命令路径不得重复，加载时校验。
- `json`：参数项可用于 `{param}` 或 `{opt,param}`，对应 schema 属性为 object 或 array；拼命令时写入紧凑 JSON，反解时按 JSON 解析。

## 匹配与反解

1. Bash 调用解析为已挂载连接器的单条直接调用（复用 `accesspolicy.DirectConnectorInvocation`），取得 argv。
2. 在 Run 冻结的 VIEW 挂载中，找该连接器子命令路径作为 argv 连续片段出现的拦截 action。没有命中就照常执行。
3. 命中后严格反解：
   - 模板字面量都必须出现；以 `-` 开头的字面量顺序不限，缺失的由重建补上。
   - `{opt,param}` 接受 `--opt value` 与 `--opt=value`；`{flag,param}` 出现即 true；`{param}` 按模板顺序取路径之后的位置参数。
   - 值按 `paramsSchema` 属性类型转换（string / integer / number / boolean，`json` 项按 JSON 解析）。
   - 出现未声明的参数、重复选项或多余位置参数即失败。
4. 反解失败或 `paramsSchema` 校验失败：不执行、不弹表单，工具结果告诉模型哪些参数不被接受。
5. 命中拦截 action 的子命令，若不是单条直接调用（管道、包装、重定向、变量展开）也拒绝，避免换写法绕过审阅。

## 运行流程

1. 发出 `awaiting.ask(mode=form)`：`view` 为该 VIEW 的 form 快照，`form = {title: action.description, data: {action, params}}`。
2. 提交 `param: {decision:"approve", data:{params}}`：服务端按 `paramsSchema` 校验，用模板重建 argv，拼成单条直接调用，经 Bash 原路执行（凭据注入、输出与审计不变）。
3. 拒绝：工具结果为拒绝原因与用户修改后的 `params`。超时同其他 form。
4. 不支持重启恢复，与其他 form 一致。

## 改动清单

Platform

- `internal/view`：`Action.Intercept`、`QueryArg.JSON` 与校验；`CLIArgs` 移入并新增反向函数，golden 用例测往返。`viewquery` / `viewaction` 改用新位置。
- `internal/llm`：`checkBashHITL` 改为按 VIEW 拦截 action 匹配；form 提交按 action 重建命令，替换 `--payload` 改写。
- 删除 `.bash-hooks`：skill 加载、`ResolveSkillRuntimeSettings` 的 hook 目录、`internal/hitl` 的规则加载与匹配、引擎上的 checker；技能目录出现 `.bash-hooks` 时加载报错。
- 文档：`VIEW连接器.md`、`HITL协议.md`、`连接器.md`、`AGENTS.md`、`README.md`。

WebClient

- 核对 HITL 宿主对连接器 form VIEW 的支持（`awaiting_init` / `awaiting_collect` / `frontend_awaiting_submit`）。

Jira 连接器（zenmind-env）

- CLI 新增 `issue worklog add-batch --payload`：自行校验、逐条执行、失败即停、输出逐条结果。
- `view.json`：`worklog` 增加 `form` 用途与 `worklog.batch`。
- `worklog.js`：form 分支读取 `params.payload` 渲染，collect 时返回编辑后的 `{params}`，隐藏页面自身提交按钮。
- mock 技能：删除 `.bash-hooks`，更新 SKILL.md。

## 待核实

- 原 mock `dangerous-commands.yml` 中 `shutdown`、`reboot`、`mkfs` 等是否都被平台 Shell 分析覆盖（default 档下这类未建模程序按 opaque 审批）。
