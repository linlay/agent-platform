# 注册与热加载（registry_hot_reload）

## 关键类
- `RuntimeResourceSyncService`
- `DirectoryWatchService`
- `AgentRegistry`
- `ModelRegistryService`
- `CapabilityRegistryService`
- `SkillRegistryService`
- `ViewportRegistryService`

## 启动阶段
- `RuntimeResourceSyncService` 将 classpath 资源同步到外部目录。
- 各 registry 在构造时执行首次 `refresh*()`。

## 运行阶段热更新
`DirectoryWatchService` 监听目录变更并去抖（500ms）：
- agents -> 仅刷新 agent
- models -> 先刷新 model，再刷新 agent（因 agent 依赖 modelKey）
- tools/skills/viewports -> 刷新对应 registry

## 一致性约束
- registry 刷新采用快照替换（volatile map），读线程无锁。
- 刷新失败保留旧快照，避免服务不可用。
