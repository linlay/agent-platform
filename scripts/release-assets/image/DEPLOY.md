# Agent Platform 镜像部署

本目录是一个离线发布包：一个 linux/amd64 镜像归档和启动它所需的全部文件。服务器只需要 Docker Engine 与 compose 插件，不需要源码或编译器。

```text
agent-platform-<version>-linux-amd64.tar.gz   镜像归档
checksums.sha256                              本目录所有文件的摘要
manifest.json                                 版本、commit、builtin 版本清单
compose.yml                                   部署用 Compose，不含 build
.env.example                                  部署参数模板
configs/*.example.*                           配置模板
load.sh                                       校验并导入镜像
```

## 1. 导入镜像

```bash
./load.sh
```

脚本先校验 `checksums.sha256`，再 `docker image load`，最后核对镜像的 revision 标签与 `manifest.json` 一致。任何一步不符都会失败。

## 2. 准备配置

```bash
cp .env.example .env && chmod 600 .env
for f in configs/*.example.yml; do cp "$f" "${f/.example/}"; done
```

启动前需要处理的四处：

| 位置 | 要做的事 |
| --- | --- |
| `.env` 的 `AP_INSTANCE_NAME` | 本实例的名字（小写字母、数字、连字符），Compose 项目名为 `agent-platform-<名字>`，在这台主机上必须唯一，设定后不要再改；未填写时 Compose 拒绝启动。同一主机上两个实例同名时，`docker compose up` 会替换掉另一个实例的容器。同时确认 `AP_PORT` 不与其他实例冲突 |
| `.env` 的 `AP_CHAT_RESOURCE_TICKET_SECRET` | 资源访问票据的签名密钥，填入随机值，例如 `openssl rand -hex 32` |
| `configs/local-public-key.pem` | 放入签发访问 token 的 RSA 公钥；模板里的示例 PEM 不是有效密钥 |
| `configs/runtime.yml` 的 `document-preview` | 填写 `api-base-url` 与 `public-base-url`，或把 `enabled` 改为 `false` |

模型、Provider 和其他能力按需在 `configs/` 与运行目录的 registries 中配置。`configs/` 以只读方式挂载，修改后需要重启容器。

## 3. 准备数据目录

```bash
mkdir -p data/runtime data/workspaces
sudo chown -R 10001:10001 data
```

容器以 UID/GID 10001 运行，数据目录必须对它可写。`data/runtime` 保存会话、Memory、知识库和凭据，`data/workspaces` 保存 Agent 的项目目录（容器内为 `/opt/workspaces/<项目>`）。要把数据放到别处，修改 `.env` 的 `AP_DATA_DIR`。

数据目录必须位于文件锁（flock）有效的本地文件系统，例如 ext4 或 xfs。Platform 用文件锁保护连接器版本目录；在不执行文件锁的挂载上（Docker Desktop 的宿主目录共享、部分网络文件系统），正在使用的版本会被误回收，表现为启动时报 `bind Agent MCP instances ... no such file or directory`。

## 4. 启动

```bash
docker compose up -d
docker compose ps        # 等到 STATUS 为 healthy
```

服务默认只发布在 `127.0.0.1:11949`，由本机的 TLS 反向代理转发。反向代理需要支持 WebSocket 升级，并关闭对 SSE 响应的缓冲。不要把 `AP_BIND_ADDRESS` 改成公网地址来直接对外提供明文 HTTP。

## 5. 运行方式

| 项 | 设置 |
| --- | --- |
| 用户 | 10001，非 root |
| 根文件系统 | 只读；`/tmp` 与 `/home/agent` 是内存盘，重启后清空 |
| 权限 | 去掉全部 capabilities，`no-new-privileges` |
| 资源 | 内存、CPU、进程数上限见 `.env` |
| 日志 | json-file，单文件 20 MB，保留 5 个 |

## 6. 升级与回滚

导入新版本的发布包（`./load.sh`），把 `.env` 的 `AP_IMAGE_VERSION` 改为新版本，再 `docker compose up -d`。回滚时改回旧版本号。升级会重启容器，未完成的任务会被取消，会话历史保留。升级前先备份 `data/`。

## 7. 验收清单

记录 `uname -m`、`docker version` 和发行版版本，然后逐项执行。把 `<端口>` 换成 `.env` 中的 `AP_PORT`。

### A. 导入

- `./load.sh` 成功；故意改动任一文件后再运行，必须失败。
- `docker image ls agent-platform` 能看到镜像。

### B. 启动与基础检查

```bash
curl -s http://127.0.0.1:<端口>/healthz
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:<端口>/api/agents
docker compose logs | grep -i -E "startup failed|panic"
ls data/runtime/ru-connectors/builtin.kanban-control/
```

- `/healthz` 返回 200，`runtime` 为 `ready`，`kbase.sidecar.available` 为 `true`。
- 无 token 访问 `/api/agents` 返回 401。
- 日志中没有启动失败。
- `ru-connectors/builtin.kanban-control/` 下有一个以摘要命名的目录，且 `docker compose ps` 显示容器没有重启过。目录缺失或首次启动失败后才成功，说明数据目录所在文件系统的文件锁无效（见第 3 节）。

### C. 加固

```bash
docker compose exec agent-platform id
docker compose exec agent-platform touch /opt/write-probe
docker inspect --format '{{.HostConfig.CapDrop}} {{.HostConfig.ReadonlyRootfs}}' "$(docker compose ps -q agent-platform)"
ss -ltnp | grep <端口>
```

- `id` 显示 `uid=10001`。
- `touch` 失败并提示只读文件系统。
- 输出为 `[ALL] true`。
- 只监听 `127.0.0.1`。

### D. builtin

```bash
docker compose exec agent-platform sh -c '
  /opt/bin/rg --version | head -1
  /opt/bin/kbx --version
  /opt/bin/pdftotext -v 2>&1 | head -1
  /opt/connectors/builtin.httpx/bin/httpx --version
  /opt/connectors/builtin.dbx/bin/dbx --version'
```

五条命令都应输出版本号。随后用带有效 token 的客户端验证：

1. 创建一个知识库，放入一份中文 PDF，索引完成后全文检索能命中。
2. 配置 embedding 模型后，向量检索能返回结果。
3. 开启 Memory，完成一个会话后生成了 summary。
4. 挂载 dbx 或 httpx 的 Agent 能完成一次实际调用。

### E. 持久化与重启

- `docker compose down && docker compose up -d` 之后，会话、知识库和 Memory 都在。
- 服务器重启后容器自动拉起并恢复为 healthy。
- 执行长任务时 `docker compose restart`，该任务显示为已取消，没有卡在运行中。

### F. 升级与回滚

有第二个版本的发布包时按第 6 节操作，确认升级后数据可用、回滚后服务正常。

### 反馈

每一段标注通过或失败。失败项附对应命令的输出和 `docker compose logs --tail 200`；其中的 token 与密钥先抹去。
