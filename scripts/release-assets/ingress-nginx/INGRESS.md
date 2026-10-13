# Agent Platform 入口（nginx）

本目录给一台主机上的多个 Agent Platform 实例提供统一的 HTTPS 入口：一张通配证书、一个 nginx 站点、每个实例一行路由。它不是独立服务，复用主机上已有的 nginx 和 certbot；实例本身仍只发布在 `127.0.0.1:<AP_PORT>`。

```text
客户端 ─ HTTPS 443 ─▶ nginx（*.<域名>，通配证书）─ 查路由表 ─▶ 127.0.0.1:<AP_PORT> ─▶ 实例容器
```

| 文件 | 用途 |
| --- | --- |
| `agent-platform-ingress.conf.template` | nginx 站点模板，由脚本渲染到 `/etc/nginx/conf.d/agent-platform-ingress.conf` |
| `routes.map.example` | 路由表格式示例；实际文件是 `/etc/nginx/agent-platform/routes.map` |
| `ap-ingress.sh` | 安装入口、增删实例路由；每次修改先 `nginx -t`，失败自动还原 |

## 1. 前提

- 主机已安装 nginx（需要 `http_v2`、`http_ssl` 模块），其 `nginx.conf` 的 `http` 段包含 `conf.d/*.conf`。
- DNS 有通配记录 `*.<域名>` 指向本机。使用 Cloudflare 时记录必须是“仅 DNS”，不能开代理。
- 通配证书只能通过 DNS 验证签发，需要 DNS 厂商的 API 凭据。

## 2. 申请通配证书（一次）

以 Cloudflare 和 certbot 为例，凭据文件格式见 certbot-dns-cloudflare 文档：

```bash
sudo certbot certonly --dns-cloudflare \
  --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
  --dns-cloudflare-propagation-seconds 60 \
  -d '<域名>' -d '*.<域名>'
```

证书位于 `/etc/letsencrypt/live/<域名>/`，由 certbot 定时器自动续期。续期后 nginx 必须重载才会使用新证书：确认 `/etc/letsencrypt/renewal-hooks/deploy/` 下有执行 `nginx -t && systemctl reload nginx` 的钩子，没有就加一个。

## 3. 安装入口（一次）

```bash
sudo ./ap-ingress.sh install <域名>
```

证书不在 certbot 默认位置时加 `--cert-dir <含 fullchain.pem 与 privkey.pem 的目录>`。升级本目录后重新执行同一条命令即可更新站点配置，路由表保留。

## 4. 增删实例

```bash
sudo ./ap-ingress.sh add linlay 11949      # https://linlay.<域名> → 127.0.0.1:11949
sudo ./ap-ingress.sh remove linlay
sudo ./ap-ingress.sh list
```

端口填该实例 `.env` 的 `AP_PORT`。同一端口不能分配给两个实例。nginx 重载不会断开已有的 WebSocket 和 SSE 连接；新路由在重载后约一秒内生效。

## 5. 行为与边界

| 项 | 行为 |
| --- | --- |
| 未登记的主机名 | 直接断开连接（444），不返回内容 |
| 转发头 | `X-Forwarded-For`、`X-Real-IP`、`X-Forwarded-Proto`、`X-Forwarded-Host` 由入口重写，客户端自带的值与 `Forwarded` 头被丢弃 |
| WebSocket / SSE | 透传升级请求；不缓冲响应；空闲超时 3600 秒 |
| 上传 | 单个请求体上限 100 MB |
| 访问日志 | `/var/log/nginx/agent-platform-ingress.access.log`，不记录查询串和请求头 |
| 80 端口 | 跳转到 HTTPS |

已知限制：

- 上游出错时 nginx 的 error log 会记录完整请求行，包括查询串。在客户端停止把 token 放进 URL 之前，error log 应按含凭据的文件保管。
- 入口只做传输层保护和路由。实例是否只接受自己 owner 的 token，由 Platform 的鉴权决定，不在这里。
- 实例停止时入口返回 502。

## 6. 验收

把 `<主机>` 换成 `<实例名>.<域名>`：

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<主机>/healthz                 # 200
curl -sS -o /dev/null -w '%{http_code}\n' https://<主机>/api/agents              # 401（未带 token）
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' http://<主机>/x        # 301 https://<主机>/x
curl -sS -o /dev/null -w '%{exitcode}\n' https://unregistered.<域名>/            # 非 0：连接被断开
echo | openssl s_client -connect <主机>:443 -servername <主机> 2>/dev/null \
  | openssl x509 -noout -subject -enddate                                        # 通配证书与有效期
```

再用客户端实际发起一次对话，确认流式输出逐段到达、WebSocket 连接保持。
