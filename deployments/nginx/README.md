# 本机 OpenIM 公网客户端网关

Nginx 与依赖组件由根目录 `docker-compose.yml` 管理。当前 OpenIM API 和 WS 是宿主进程，容器经 `host.docker.internal` 访问它们。

| 用途 | 新网关入口 | 默认上游 |
| --- | --- | --- |
| 客户端 API | `http://localhost:10080` | `host.docker.internal:10002` |
| 客户端 WS | `ws://localhost:10081` | `host.docker.internal:10001` |
| 业务服务管理 API | 原内网地址，例如 `http://localhost:10002` | 不经过公网客户端白名单 |

此配置先增加新入口，不修改原 OpenIM 监听端口、业务服务地址或现有 CF 配置。原公网入口暂时不受这个白名单约束；正式切换时，让公网流量进入新网关，并限制原 `10001`、`10002` 的公网直连。公网客户端最终使用 HTTPS/WSS，TLS 可继续由现有入口承担。

## 启动与验证

在 OpenIM Server 仓库根目录执行：

```sh
docker compose config --quiet
docker compose up -d --no-deps nginx
docker compose exec nginx nginx -t
curl --fail http://localhost:10080/healthz
```

SDK 的 `apiAddr` 设置为 `http://localhost:10080`，`wsAddr` 设置为 `ws://localhost:10081`。真机使用本机可达的 LAN 地址。API 地址没有 `/api` 前缀，WS 使用根路径。

健康检查只检查 Nginx 自身。完整验证还需确认原 `10002`、`10001` 的 OpenIM 进程运行，并通过新入口验证 SDK 登录、好友/群同步、已读、清理会话、上传文件和消息接收。

可重复运行网关集成验证：

```sh
python3 deployments/nginx/test_gateway.py
```

验证脚本使用 Python 标准库和 Docker CLI，启动模拟上游与临时 Nginx 容器，验证接口拦截、路径规范化、方法/CORS、认证头及请求体转发、对象 URL，以及 WS 握手和二进制帧转发。结束后删除临时容器，不读写真实 OpenIM 数据；它不代替上述 SDK 联调。

## 公网接口范围

`client-api-allowlist.map` 按精确路径放行 SDK 所需的读取、增量同步、会话设置、已读、清理己方消息、推送登记和文件上传控制接口。REST 仅允许 POST；OPTIONS 仅在白名单路径返回预检响应；文件 GET `/object/*` 用于 OpenIM 对象重定向。其余接口默认返回 HTTP 403。

好友增删、申请发起/审批、备注、拉黑、资料修改、群关系/权限修改、消息发送/撤回、token 签发、管理及监控接口不开放给公网客户端。`/friend/is_friend` 当前没有参与者权限校验，暂不放行；未来直接查询双向关系前须补齐该校验。白名单不代替 OpenIM 自身的 token、owner、群角色等授权检查。

WebSocket 端口仅接受根路径上的 GET WebSocket 握手。Nginx 不解析 WS 数据帧；消息发送等业务规则仍由现有 OpenIM 前置回调执行。API 和 WS 访问日志不记录 query，避免记录握手 token。

更新白名单后：

```sh
docker compose exec nginx nginx -t
docker compose exec nginx nginx -s reload
```

模板或环境变量更新后，用 `docker compose up -d --force-recreate nginx` 重新生成配置。

## 环境变量

| 变量 | 默认值 |
| --- | --- |
| `OPENIM_NGINX_IMAGE` | `nginx:1.30.5-alpine` |
| `OPENIM_NGINX_BIND_IP` | `0.0.0.0` |
| `OPENIM_NGINX_API_PORT` | `10080` |
| `OPENIM_NGINX_WS_PORT` | `10081` |
| `OPENIM_NGINX_API_UPSTREAM` | `host.docker.internal:10002` |
| `OPENIM_NGINX_WS_UPSTREAM` | `host.docker.internal:10001` |
| `OPENIM_NGINX_PUBLIC_API_URL` | 空；使用当前请求的协议与 Host |

正式接入外层 HTTPS 入口时，设置 `OPENIM_NGINX_PUBLIC_API_URL=https://实际的API域名`，然后重建 Nginx 容器。OpenIM 使用代理传入的 `X-Request-Api` 生成对象下载 URL；这个变量防止 TLS 在外层终止后生成 HTTP URL。

如后续 OpenIM 服务也容器化，改 upstream 为同一 Compose 网络中的服务名与端口，例如 `openim-api:10002`。Nginx 代理宿主进程时，上游必须能从 Docker 网络访问；把上游绑定到宿主 loopback 前，先验证 Docker 到宿主 loopback 的可达性。
