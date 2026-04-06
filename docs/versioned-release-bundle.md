# 版本化离线打包方案

本文件沿用参考仓库的主题，但当前 Go 仓库还没有实现 Java 版那套完整的 `VERSION + dist/release + release-assets` 离线打包链路。

## 1. 当前状态

目前已具备：

- `Dockerfile`
- `compose.yml`
- `make run`
- `make test`
- `docker build`
- `docker compose up --build`

目前尚未具备：

- 根目录 `VERSION`
- `make release`
- `scripts/release.sh`
- `scripts/release-assets/`
- `dist/release/` 版本化 bundle 输出

## 2. 当前可用交付方式

现阶段建议的交付方式只有两种：

### 方式 A: 镜像交付

```bash
docker build -t agent-platform-runner-go:latest .
```

然后把镜像推送到镜像仓库，部署侧通过标准容器流程拉取。

### 方式 B: 仓库 + Compose 交付

交付内容：

- 源码仓库
- `.env.example`
- `compose.yml`
- `configs/*.example.yml`

部署侧步骤：

```bash
cp .env.example .env
docker compose up --build -d
```

## 3. 若后续补齐版本化打包，建议保持的结构

建议与参考仓库保持同样的发布骨架：

```text
VERSION
scripts/
  release.sh
  release-assets/
dist/
  release/
```

建议目标产物命名：

- `agent-platform-runner-go-vX.Y.Z-linux-amd64.tar.gz`
- `agent-platform-runner-go-vX.Y.Z-linux-arm64.tar.gz`

## 4. 建议的发布内容

若后续引入 bundle，建议包含：

- 预构建镜像 tar
- `compose.release.yml`
- `start.sh`
- `stop.sh`
- `.env.example`
- `configs/*.example.yml`
- 简短部署说明

## 5. 当前结论

Go 版目前适合开发期与内部联调，不适合宣称已经具备参考仓库那种标准化离线发布能力；在真正补齐脚本前，这份文档只定义目标方向，不应被当作“现状说明”。
