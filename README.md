# tf-gateway

FishMatrix（摸鱼矩阵）统一网关服务 —— 基于 Go + Gin，单二进制部署。

- 角色：系统统一入口，后续承接路由转发、权限校验、流量伪装
- 端口：`8080`（可用 `PORT` 环境变量覆盖）
- 健康检查：`GET /healthz`

## 本地开发

```bash
go run .            # 启动开发服务
go test ./...       # 运行测试
golangci-lint run   # 静态检查（需安装 golangci-lint）
```

## 构建

```bash
go build -trimpath -ldflags "-s -w -X main.version=v0.1.0" -o bin/tf-gateway .
```

## 制品与镜像

合并到 `master` 后自动发版（见 GitHub Releases）：

- 二进制：`tf-gateway_vX.Y.Z_linux_amd64` / `_linux_arm64` / `_windows_amd64.exe`（附 SHA256SUMS）
- Docker 镜像：`ghcr.io/take-fish/tf-gateway:vX.Y.Z`（以及 `:latest`）

## 分支模型

- `master`：保护分支，只接受 PR（CI 必须通过），合并后自动发版
- `test`：联调分支，可直接推送
- 开发流：`feature/*` → PR 到 `test` → 联调 → PR 到 `master` → 自动发版
