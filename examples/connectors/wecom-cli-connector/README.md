# 企业微信 CLI 连接器

按连接器核心规范封装官方 `@wecom/cli@1.2.1`，安装使用平台版本私有 npm prefix，不写用户全局目录。登录和业务调用共用平台为当前用户分配的 HOME。

`auth init --noninteractive --no-browser` 返回企微二维码页面，由平台展示。`auth show --status` 只输出授权状态，避免输出机器人材料。官方 CLI 当前没有退出命令，`unAuth` 幂等清理私有 HOME 下的 `.config/wecom`；平台应报告上游撤销不支持，之后完成本地环境清理。

包不携带凭据。平台升级不自动接管旧共享账号；已有账号迁移须明确当前用户归属。

官方 npm 支持 macOS/Linux x64及arm64、Windows x64；Windows arm64在固定制品不可用时明确报告不支持。Node运行时需由宿主提供。

本目录仅为连接器声明示例；发布时可同时携带官方所需技能。示例尚未上传市场。
