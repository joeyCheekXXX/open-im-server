# GLB 平台真实服务验收

配套协议：`codex/glb-07-client-platform`，提交 `7fe3e97ac8ee6781421d33eeffe6f59f5cba6f2a`。平台编号 13，名称 GLB，终端类别 GLB；1–12 的身份不变。

服务端原有平台白名单通过协议 `PlatformID2Name` 工作，按平台连接索引和按令牌撤销不需要另加分支。必须用上述协议重新编译所有 OpenIM 服务，并配套业务后端 GLB 平台映射、业务 worker 及其数据库。不要只换网关或只改业务枚举。

## 策略核对

- `policy: 1`：同平台互踢；GLB 与 1/2/5 共存，新 GLB 只替换旧 GLB。本规格要求部署采用此策略（仓库默认已是 1）。
- `policy: 4`：按类别互踢；GLB 独立类别不与其他类别混合，但既有 iOS/Android 同属 Mobile，不满足微聊三端共存。
- `policy: 5`：PC 与其他端策略会把 GLB 与移动/Web 收进非 PC 集合，不满足共存要求。
- `policy: 0`：不互踢，不满足由 OpenIM 执行同平台替换要求。其他策略也不是本工单支持的部署配置。

保留这些策略的原有行为，没有因 GLB 改写所有 OpenIM 用户的策略语义。

## 自动验证

从服务端任务 worktree 执行；使用独立 modfile 指向协议任务 worktree，避免更改原有 checkout 或全局 Go 配置：

```sh
cp go.mod /tmp/glb07-server.mod
cp go.sum /tmp/glb07-server.sum
GOPROXY=off GOSUMDB=off go mod edit -modfile=/tmp/glb07-server.mod -replace=github.com/openimsdk/protocol=/Users/joey/Workspace/pitaya-chat/openim-forks/protocol/.worktrees/codex/glb-07-client-platform
GOPROXY=off GOSUMDB=off go test -modfile=/tmp/glb07-server.mod ./internal/msggateway/test ./internal/msggateway ./internal/rpc/auth
```

连接参数公开入口在旧协议下拒绝 13，配套新协议下通过；这只是本地回归，不等于真实服务验收。

## 真实联调

先启动隔离的微聊业务 API/worker 和配套 OpenIM 服务，保持默认 `policy: 1`。使用两个专用微聊测试账号；探针会替换它们在 iOS、Android、Web、GLB 上的会话，结束时注销本次签发的会话。不得填真实使用中的账号。无需群数据，不发送消息。

通过环境提供：`GLB_PROBE_BASE_URL`（业务 API 根地址）、`GLB_PROBE_ACCOUNT_A`、`GLB_PROBE_PASSWORD_A`、`GLB_PROBE_ACCOUNT_B`、`GLB_PROBE_PASSWORD_B`，并设置 `GLB_PROBE_DEDICATED_ACCOUNTS=yes`。不要将密码写入命令历史或提交到仓库。

```sh
GOPROXY=off GOSUMDB=off go test -modfile=/tmp/glb07-server.mod ./scripts/glbplatform -run TestGLBPlatformRealServices -count=1 -v
```

未设置地址时会明确 SKIP，不能记为验收通过。探针验证真实 HTTP 登录、四平台 WebSocket 共存、GLB 新登录、旧业务 401、旧连接关闭、旧 IM 令牌返回 1506、另一账号不受影响、会话查询及共存连接 Ping/Pong。运行 API/网关日志可能含令牌，应只在隔离环境运行并按现有凭证规范管理日志。

## 本轮结果

本轮本地公开入口及撤销回归通过，配套协议的 `go build ./cmd/...` 全部服务入口编译通过；真实探针未运行。现有 10001/10002 是 GoLand 启动的服务，未停止、替换或重启。未提供由本次配套协议构建的隔离运行环境及两个专用测试账号，因此真实跨平台共存与旧凭证拒绝仍待验收。fork 任务分支未合入 `codex/develop`。
