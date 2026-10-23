# tf-gateway

FishMatrix（摸鱼矩阵）统一网关 —— Go 标准库 HTTP 反向代理，路由与实例列表来自 Nacos，启动不依赖 Nacos 可用。

- 角色：统一入口、按路由前缀反向代理、WebSocket 透传、按服务名做实例发现
- 认证：校验 Bearer token，向用户中台确认身份，向下游注入 HMAC 签名的 `X-FM-*` 身份头
- 端口：`8080`（`PORT` 覆盖）
- 健康检查：`GET /healthz`、`GET /readyz`
- 依赖：Go 1.25；`nacos-sdk-go/v2`

## 工作方式

1. 启动时先加载内置默认路由，HTTP 端口立即可用。
2. 后台连接 Nacos，读取 `tf-gateway.yaml`（默认 `fm-prod` 命名空间、`FM` 分组）并订阅变更；配置校验失败时保留上一份有效快照。
3. 按路由中的 `service` 订阅 Nacos 服务实例（默认 `DEFAULT_GROUP`），只选择 `Healthy && Enable && Weight > 0` 的实例，轮询转发。
4. Nacos 不可用或服务无实例时，若 `fallback-enabled: true`，回退到 `TF_*_URL` 静态地址。
5. 代理重试只针对 `GET/HEAD/OPTIONS` 等幂等方法且仅限连接类错误；`POST/PUT/PATCH/DELETE` 不重试。

## 路由配置

Nacos 配置：`dataId=tf-gateway.yaml`，`group=FM`，`namespace=fm-prod|fm-test`。

```yaml
version: 1

gateway:
  discovery:
    enabled: true
    group: DEFAULT_GROUP
    clusters: [DEFAULT]
    cache-ttl-seconds: 10
    refresh-interval-seconds: 10
    fallback-enabled: true

  http:
    connect-timeout-ms: 2000
    response-header-timeout-ms: 30000
    idle-conn-timeout-ms: 90000

  retry:
    enabled: true
    max-attempts: 2
    methods: [GET, HEAD, OPTIONS]

  routes:
    - id: user
      prefix: /api/v1/user
      service: tf-user

    - id: chat-ws
      path: /ws/chat
      service: tf-chat
      websocket: true
```

约束：

- 每条路由必须且只能配置 `path`（精确匹配）或 `prefix`（最长前缀匹配）之一
- `service` 必须形如 `tf-<name>`
- `websocket: true` 必须使用 `path`
- 路由 ID、路径、前缀不得重复
- 解析采用严格 YAML，未知字段会拒绝整份配置

配置变更由 Nacos 监听推送，无需重启网关；变更后新请求立即使用新快照。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PORT` | `8080` | 监听端口 |
| `NACOS_ADDR` | `127.0.0.1:8848` | Nacos 地址 |
| `NACOS_NAMESPACE` | `fm-prod` | 命名空间 |
| `GATEWAY_NACOS_USERNAME` | 回退 `NACOS_USERNAME` | 建议使用只读账号 |
| `GATEWAY_NACOS_PASSWORD` | 回退 `NACOS_PASSWORD` | 只读账号密码 |
| `GATEWAY_CONFIG_DATA_ID` | `tf-gateway.yaml` | 路由配置 dataId |
| `GATEWAY_CONFIG_GROUP` | `FM` | 路由配置分组 |
| `GATEWAY_DISCOVERY_GROUP` | `DEFAULT_GROUP` | 服务发现分组 |
| `GATEWAY_DISCOVERY_ENABLED` | `true` | 关闭后只用静态地址 |
| `TF_USER_URL` 等 | `http://127.0.0.1:810x` | 静态回退地址 |
| `GATEWAY_AUTH_ENABLED` | `true` | 是否启用统一认证 |
| `FM_IDENTITY_SECRET` | 空 | 身份头 HMAC 密钥；启用认证时必填，缺失会导致受保护请求返回 503 |
| `GATEWAY_AUTH_USER_SERVICE` | `tf-user` | 用于 introspection 的服务名 |
| `GATEWAY_AUTH_CACHE_SECONDS` | `30` | token 校验结果内存缓存 TTL |
| `GATEWAY_AUTH_CONNECT_TIMEOUT_MS` | `800` | 调用用户中台连接超时 |
| `GATEWAY_AUTH_READ_TIMEOUT_MS` | `2000` | 调用用户中台读取超时 |
| `GATEWAY_AUTH_PUBLIC_PATHS` | 空 | 追加公开路径，逗号分隔，支持 `/**` 与 `*` |

## 统一认证

受保护请求必须携带 `Authorization: Bearer <token>`。网关在命中路由后、解析上游实例前完成校验，未通过不会触达业务服务。

1. 网关调用 `tf-user` 的 `GET /api/v1/user/auth/me` 校验会话并获取角色与权限，成功结果按 token 的 SHA-256 缓存 `GATEWAY_AUTH_CACHE_SECONDS` 秒。
2. 网关生成规范串并做 HMAC-SHA256 签名，向下游注入以下请求头（客户端的同名头一律先剥离）：
   - `X-FM-User-Id`、`X-FM-Username`、`X-FM-Nickname`、`X-FM-Role`
   - `X-FM-Roles`、`X-FM-Permissions`（逗号分隔）
   - `X-FM-Exp`（epoch 秒，默认 120 秒有效）、`X-FM-Signature`
3. 规范串：`uid|b64url(username)|b64url(nickname)|b64url(role)|roles|permissions|exp`，Base64 均为 URL-safe 无填充。业务服务通过 `tf-common` 的 `IdentitySigner` 校验，不自行信任请求头。
4. 登录、注册、`/meta`、各服务 `/health`、用户中台管理页静态资源与监控接口默认公开；WebSocket 路由由业务服务在握手时校验 ticket，网关不拦截。

错误码：

| 状态 | 错误码 | 说明 |
|---|---|---|
| 401 | `UNAUTHORIZED` | 缺少 token 或会话已失效 |
| 503 | `AUTH_UNAVAILABLE` | 用户中台不可用，按 fail-closed 处理 |
| 503 | `AUTH_CONFIG_MISSING` | 网关未配置 `FM_IDENTITY_SECRET` |

密钥只通过环境变量注入，不得写入 Nacos 配置或仓库。网关与各业务服务必须使用同一个密钥。

## 健康检查

- `GET /healthz`：`ok` / `degraded` / `failed`，返回路由数量与 Nacos 配置、发现状态
- `GET /readyz`：至少加载到一份有效路由即 `ready`
- `GET /api/v1/monitor/metrics`：保留的演示监控接口

## 错误响应

| 状态 | 错误码 |
|---|---|
| 401 | `UNAUTHORIZED` |
| 404 | `NOT_FOUND` |
| 502 | `BAD_GATEWAY` |
| 503 | `AUTH_UNAVAILABLE` / `AUTH_CONFIG_MISSING` |
| 503 | `SERVICE_UNAVAILABLE` |

## 本地开发

```bash
go run .            # 静态回退模式也可直接转发到本机 8101-8105
go test ./...
go vet ./...
go build .
```

## 构建

```bash
go build -trimpath -ldflags "-s -w -X main.version=v0.1.0" -o bin/tf-gateway .
```

镜像构建见 `Dockerfile`；测试环境可用 `tf-deploy/scripts/build-test-local.sh gateway`。

## 分支模型

- `master`：保护分支，PR + CI，合并后自动发版
- `test`：联调分支，开发分支直接合入
- 开发流：`feature/*` → `test` 联调 → PR 合 `master` → 自动发版
