# relay 自部署指南

本文介绍如何构建并以单进程方式运行 `anyview-next`，包含二进制构建、启动参数、systemd 托管、反向代理及健康检查。

## 1. 构建产物

```sh
# 1) 构建前端静态产物（输出到 web/.output/public）
cd web && bun install --frozen-lockfile && bun run generate && cd ..

# 2) 构建静态 Go 二进制（按目标架构调整 GOOS / GOARCH）
CGO_ENABLED=0 go -C relay build -trimpath -ldflags "-s -w" -o /tmp/anyview-relay .
```

## 2. 目录与启动参数

建议将二进制、静态页面和持久化文件放在同一独立目录（例如 `/opt/anyview-next/`）：

```text
/opt/anyview-next/
  relay               # 编译产出的 Go 二进制
  web/                # web/.output/public 复制到此目录
  state.json          # 运行时自动生成（含用户会话 Token，勿入库）
  relay-cache.json    # 运行时自动生成（题面/模板/目录缓存，勿入库）
```

启动命令：

```sh
/opt/anyview-next/relay \
  -listen 127.0.0.1:8787 \
  -state /opt/anyview-next/state.json \
  -cacheFile /opt/anyview-next/relay-cache.json \
  -web /opt/anyview-next/web \
  -rpm 20 \
  -wafCooldown 30m
```

参数说明：

- `-web` 启用后，`relay` 自动为 `/_nuxt/*` 设置不可变长缓存、为 `index.html` 与 SPA 回退路由设置 `no-cache`，并对可压缩响应启用 Gzip。同源部署无需开启 CORS；若前后端分离部署，可通过 `-cors https://your-frontend.example.com` 指定允许的 Origin。
- 若部署环境需要绕过默认 DNS 直连指定源站 IP，可通过 `-dial <IP>:443` 覆盖 TCP 目标地址（HTTPS SNI 与 `Host` 仍保留 `-upstream` 域名，HTTP 与 exam WebSocket 均生效）。
- 收到 `SIGINT` 或 `SIGTERM` 时，`relay` 会发送 exam 停表帧、关闭 WebSocket 并将未落盘缓存同步写入磁盘。
- 仅预览界面或离线测试时可加 `-mock`，该模式不会读写 `-state` 与 `-cacheFile`。

## 3. systemd 服务示例

新建 `/etc/systemd/system/anyview-next.service`：

```ini
[Unit]
Description=anyview-next relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/anyview-next
ExecStart=/opt/anyview-next/relay \
  -listen 127.0.0.1:8787 \
  -state /opt/anyview-next/state.json \
  -cacheFile /opt/anyview-next/relay-cache.json \
  -web /opt/anyview-next/web \
  -rpm 20 \
  -wafCooldown 30m
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

启用并启动：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now anyview-next
```

## 4. 反向代理（Caddy / Nginx）

### Caddy

```caddyfile
anyview.example.com {
	reverse_proxy 127.0.0.1:8787
}
```

### Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name anyview.example.com;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 300s;
    }
}
```

> `proxy_read_timeout` 建议不低于 `300s`，以覆盖编译、多组判题和等待输入链路的最长耗时。

## 5. 健康检查

```sh
curl -s http://127.0.0.1:8787/api/health
```

返回 `{ok, mock, upstream, uptimeSec, limiter, sessions, cache, web}`，可用于确认 `-web` 是否就绪、限速窗口占用及 WAF 冷却剩余时长。
