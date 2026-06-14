# deployment.md — 部署指南

## 本地开发

```bash
go build -o aexp ./cmd/aexp
./aexp init
./aexp serve --port 8080
```

如修改 React 控制台，先构建前端产物：

```bash
pnpm --dir web install
pnpm --dir web build
go build -o aexp ./cmd/aexp
./aexp serve --port 8080
```

旧 UI 在 `http://127.0.0.1:8080/`，新 UI 在
`http://127.0.0.1:8080/ui-v2/`。新 UI 深链（例如
`/ui-v2/runs/<run_id>`）会回落到 React SPA。

## Docker Compose

仓库提供 `Dockerfile` 和 `compose.yaml`。Compose 会把 SQLite 数据库放在
命名 volume `aexp-data` 中，容器内路径为 `/data/aexp/aexp.db`。

```bash
docker compose up --build
```

默认端口映射为 `8080:8080`。服务启动后：

```text
http://127.0.0.1:8080/
http://127.0.0.1:8080/ui-v2/
```

容器默认只挂载 `~/.ssh` 为只读，方便复用 SSH key。需要额外
ProxyCommand 工具、私有 key 或不同数据目录时，调整 `compose.yaml` 的
volumes/command 即可。

## 服务器部署

### 编译

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -o aexp ./cmd/aexp

# 传到服务器
scp aexp user@server:/usr/local/bin/
```

### systemd service

```ini
# /etc/systemd/system/aexp.service
[Unit]
Description=aexp - Agent Experiment Control Plane
After=network.target

[Service]
Type=simple
User=ziwu
ExecStart=/usr/local/bin/aexp serve --port 8080 --db /data/aexp/aexp.db
Restart=on-failure
RestartSec=5
WorkingDirectory=/data/aexp

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable aexp
sudo systemctl start aexp
```

### 反向代理（nginx）

```nginx
server {
    listen 80;
    server_name aexp.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 86400;  # WebSocket 长连接
    }
}
```

WebSocket 需要 `Upgrade` 和 `Connection` header，nginx 配置里必须有。

## 数据目录

```
/data/aexp/
  aexp.db          # SQLite 数据库
  logs/            # 本地日志缓存（可选）
  backups/         # 自动备份
```

## 数据备份

SQLite 备份很简单，复制文件即可：

```bash
# 手动备份
cp /data/aexp/aexp.db /data/aexp/backups/aexp_$(date +%Y%m%d).db

# 定时备份（crontab）
0 3 * * * cp /data/aexp/aexp.db /data/aexp/backups/aexp_$(date +\%Y\%m\%d).db
```

也可以用 SQLite 的 `.backup` 命令（在运行时安全复制）：

```bash
sqlite3 /data/aexp/aexp.db ".backup /data/aexp/backups/aexp_$(date +%Y%m%d).db"
```

## 更新

```bash
# 停止服务
sudo systemctl stop aexp

# 替换二进制
scp aexp_new user@server:/usr/local/bin/aexp

# 启动（SQLite migration 自动执行）
sudo systemctl start aexp
```

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AEXP_DB` | `~/.aexp/aexp.db` | 数据库路径 |
| `AEXP_PORT` | `8080` | HTTP 端口 |
| `AEXP_SSH_KEY` | `~/.aexp/id_ed25519` | 默认 SSH key |
| `AEXP_MONITOR_INTERVAL` | `10s` | 资源轮询间隔 |
| `AEXP_LOG_LEVEL` | `info` | 日志级别 |

优先级：CLI flag > 环境变量 > 配置文件 > 默认值。
