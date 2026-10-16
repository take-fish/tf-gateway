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

## 分支模型

- `master`：保护分支，只接受 PR（CI 必须通过，squash 合并），合并后自动发版
- `test`：联调分支，开发分支直接合入（不走 PR），可直接推送
- 开发流：`feature/*` 开发 → 直接合入 `test` 联调 → 验证 OK 后从开发分支开 PR 合 `master` → 自动发版
