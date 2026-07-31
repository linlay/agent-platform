# Agent 运行时组装

## 定位

Agent Platform 将可编辑事实源与执行目录分离：

```text
<AP_RUNTIME_DIR>/
├── agents/                         # Agent 定义与 Agent 自有 Skill
├── skills-market/                  # 共享 Skill
└── ru-agents/                      # Platform 生成，禁止人工编辑
    ├── .staging/
    └── <agentKey>/
        ├── agent.yml
        ├── SOUL.md
        ├── AGENTS.md
        ├── skills/
        └── .config/
```

`agents/` 和 `skills-market/` 只在 Catalog 管理、编辑和组装阶段读取。Query、Host Tool、Workspace Terminal、Container Hub 与 Skill runtime 统一使用 `ru-agents/<agentKey>`。`AgentConfigDir`、Admin Source、Agent CRUD 和“打开配置目录”仍指向原始 `agents/`。

`ru-agents` 不是来源追踪系统：不生成版本目录、Skill lock、provenance 或来源 API，也不进入 release bundle、`zenmind-env/package.sh` 产物或环境 overlay。服务启动或 Catalog 热重载时可从事实源完整重建。

## 路径配置

`paths.ru-agents-dir` 默认解析为 `<AP_RUNTIME_DIR>/ru-agents`，只支持 `configs/runtime.yml`，没有独立环境变量：

```yaml
paths:
  ru-agents-dir: ./runtime/ru-agents
```

该路径会解析为绝对路径，并且不能是文件系统根，也不能与 agents、skills-market、teams、chats、memory、kbase、registries、tools、owner、root、automations 或 pan 等根目录相同或互相包含。

## Skill 来源选择

`skillConfig.skills` 仍只声明 Skill ID，不增加 `source`：

1. `<agentsDir>/<agentKey>/skills/<id>` 存在时，必须是带合法 `SKILL.md` 的 Agent 自有 Skill。
2. 本地目录存在但不合法时，Agent 无效，不回退市场。
3. 本地不存在时，从 `<skills-market>/<id>` 读取。
4. 两处都不存在或市场 Skill 非法时，Agent 无效。
5. 重复 ID 保留第一次。

选中的 Skill 会完整复制到 `ru-agents/<agentKey>/skills/<id>`，包括 `SKILL.md`、`.bash-hooks`、`.runtime-env.json`、scripts、references 和 assets。Standalone YAML Agent 会在运行目录生成规范的 `agent.yml`，只能使用市场 Skill。

## `.config` 合并

每个选中 Skill 根目录的 `.config/**` 自动合入最终 Agent `.config/**`：

- Skill–Skill 同路径文件内容完全相同：允许。
- 同路径内容不同：Agent 无效。
- 文件/目录结构冲突：Agent 无效。
- Windows 大小写折叠后的路径冲突：Agent 无效。
- Agent 自己的 `.config` 最后应用，可覆盖同名文件，也可用文件或目录替换 Skill 冲突子树。

`.config` 只适合 Skill 可分发的非敏感默认值。真实凭据应放在 Agent 私有 `.config`、部署 Secret 或环境变量中。生成目录及文件使用私有权限，Platform 不通过 Catalog API 回显配置内容。

`.runtime-env.json` 不使用上述冲突规则。运行时顺序保持：

```text
Agent runtimeConfig.env
  < Skill 1 .runtime-env.json
  < Skill 2 .runtime-env.json
  < ...
```

后声明 Skill 覆盖前面的同名键。`AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR` 始终由 Platform 最后注入，Agent、Skill 和调用级 env 都不得声明。

## 发布与热重载

启动时 Platform 无条件删除并重建整个 `ru-agents/`，不会复用上次进程遗留的稳定目录或 `.staging`；随后为每个 Agent 建立候选目录，重新解析 Agent 和 Skill runtime 内容，全部校验成功后才安装到新的稳定目录。启动组装失败的 Agent 不会留下旧执行副本。

运行中的热重载不会清空整个根目录。稳定根 `ru-agents/<agentKey>` 不改名：

- 新文件和替换文件通过同目录临时文件加 rename 安装。
- 新内容先安装，陈旧内容最后删除。
- 不提供跨文件快照，也不保留历史版本。
- 单 Agent 组装失败只隔离该 Agent，其他 Agent 继续发布。
- 热重载候选校验失败不修改该 Agent 的稳定目录，但新 Catalog 不再发布其定义。
- Agent 删除后清理对应稳定目录；活跃 Run 不主动中断。

`agents/`（包括 Agent 自有 Skill）或 `skills-market/` 变化都会触发全量 Agent 重组。`ru-agents/` 本身不加入 watcher，避免生成循环。既有 QuerySession 的 prompt、env 和 Team snapshot 不重算；后续打开的 Skill 文件、脚本、hook 和 `.config` 会读取稳定目录中的新内容。

## Sandbox 与保留变量

Container Hub：

- `/agent` -> `ru-agents/<agentKey>`，`ro`
- `/skills` -> `ru-agents/<agentKey>/skills`，`ro`
- 显式 `platform: agents` 的 `/agents` -> `ru-agents`
- 显式 `platform: skills-market` 仍挂共享市场，只保留原有显式挂载语义

Host Tool 与 Workspace Terminal：

```text
AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/.config
AP_WORKSPACE_DIR=<canonical workspace>
AP_CHAT_DIR=<chatsDir>/<chatId>
```

Container 中三个值分别是 `/agent/.config`、`/workspace`、`/chat`。Workspace/Chat 双根和 KBASE 的 `runtimeConfig.workspaceRoot` 契约不受 Agent 组装影响。

## Office 配置迁移

Office bridge 等可分发默认配置应放在对应市场 Skill 的 `.config`：

```text
skills-market/zoffice-docx/.config/httpx/office-docx-bridge.toml
skills-market/zoffice-pptx/.config/httpx/office-pptx-bridge.toml
skills-market/zoffice-xlsx/.config/httpx/office-xlsx-bridge.toml
```

Agent 原始 `.config/httpx/` 只保留真正的 Agent 专属覆盖。
