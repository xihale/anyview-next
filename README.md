# anyview-next

AnyView（`anyview.gdut.edu.cn`）的第三方客户端，由 **Go relay 中继后端 + Nuxt 4 SPA + CodeMirror 6** 组成，围绕「题库栏 → 题面 → 编码 → 运行判题 / 调试」组织做题流程。

```text
┌─────────────┐   同源 /api   ┌───────────────────────────┐   HTTPS / WSS   ┌──────────────┐
│  Nuxt SPA   │ ───────────→ │  Go relay (:8787)         │ ─────────────→  │ AnyView 上游 │
│ CodeMirror  │              │  auth · cache · exam · gdb│                 │              │
└─────────────┘              └───────────────────────────┘                 └──────────────┘
```

## 核心特性

- **会话与安全隔离**：登录通过 RSA 公钥加密转发，密码不落盘；`relay` 统一注入会话绑定的 `X-Forwarded-For` 与 `User-Agent`，按浏览器 `an_sid` Cookie 隔离多账号并发会话。
- **WAF 限速与冷却保护**：所有上游请求经过滑动窗口限速器（`-rpm`），命中 WAF 拦截页特征（`403` / `429`）时自动进入全局静默冷却（`-wafCooldown`），期间本地拒绝上游请求防止封禁续期。
- **题面缓存与离线兜底**：共享题面与模板命中即秒回并后台异步校验；题库列表网络优先，上游异常或冷却期间自动回退本地缓存。
- **后台计时托管**：由 `relay` 维护 exam WebSocket 连接、40s 心跳与断线重连，切题或关闭页面时自动切换/停止计时。
- **编译诊断与在线调试**：集成 C/C++ 编译判题（GCC 错误解析到编辑器行号）、标准输入喂入与 OJ 服务端 GDB 调试镜像（断点、单步、变量树与调用栈）。
- **单进程生产托管与 Mock**：支持 `-web` 直接托管构建后的静态 SPA（带长缓存、`no-cache` 入口与 Gzip 压缩），也支持 `-mock` 离线假上游预览和开发。

## 快速开始（自部署）

### 环境要求

- **Go 1.26+**
- **[Bun](https://bun.sh/)**（仅构建前端静态产物时需要）

### 生产单进程部署

```sh
# 1. 安装前端依赖并生成静态 SPA（产物位于 web/.output/public）
cd web && bun install --frozen-lockfile && bun run generate && cd ..

# 2. 编译 Go relay 单二进制（产物为 relay/relay）
make relay-build

# 3. 启动服务（托管静态页面 + 同源 /api）
./relay/relay \
  -listen 127.0.0.1:8787 \
  -state ~/anyview-next-state.json \
  -cacheFile ~/anyview-next-cache.json \
  -web ./web/.output/public
```

如需公网 HTTPS 访问，建议在前面配置 Caddy 或 Nginx 反向代理（配置示例见 [relay/DEPLOY.md](relay/DEPLOY.md)）。收到 `SIGINT` 或 `SIGTERM` 时，`relay` 会自动发送停表帧、关闭 WebSocket 并同步刷新缓存到磁盘。

### 本地开发

```sh
make dev          # 并行启动 relay（air 热重载，:8787）与 nuxt dev（默认 :3000，/api 代理到 8787）
make relay        # 编译 relay 并托管 web/.output/public（需先执行 make web-build）
make relay-mock   # 启动 -mock 离线假上游，不访问真实 AnyView
make web-build    # 生成静态 SPA 到 web/.output/public
make test         # 运行 Go (-race) 与前端单元测试
make clean        # 清理构建产物与临时目录
```

> 说明：`make dev` 未检测到 `air` 时会自动安装到用户目录；`make relay` 默认将 `-state` 放在 `~/anyview-next-state.json`，避免重启或 `/tmp` 清理后丢失登录态。

## 常用配置项（`relay`）

参数定义见 [`relay/main.go`](relay/main.go)：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8787` | HTTP 监听地址 |
| `-upstream` | `https://anyview.gdut.edu.cn` | 上游服务基址 |
| `-dial` | `""` | 可选，强制 TCP 连接地址 `ip:port`（SNI 与 `Host` 仍使用 `-upstream` 域名，HTTP 与 exam WS 均生效） |
| `-xff` | `183.236.19.198` | 固定注入的 `X-Forwarded-For`（登录与后续请求必须一致） |
| `-ua` | Chrome 126 Linux UA | 固定注入的 `User-Agent` |
| `-state` | `relay-state.json` | 会话状态持久化文件（含 Token，勿入库；`-mock` 模式不读写） |
| `-cache` | `true` | 是否启用题面/样板/题库缓存池 |
| `-cacheFile` | `relay-cache.json` | 缓存持久化文件（置空则仅存内存，勿入库） |
| `-rpm` | `20` | 滚动 60s 窗口内最多放行的上游请求数（`0` 表示不限速） |
| `-wafCooldown` | `30m` | 命中 WAF 拦截特征后的全局静默冷却时长 |
| `-web` | `""` | 静态 SPA 目录路径（`/_nuxt/*` 长缓存、`index.html` `no-cache`、自动 Gzip） |
| `-cors` | `""` | 可选，允许跨域的 Origin（逗号分隔或 `*`；同源部署无需设置） |
| `-mock` | `false` | 启用离线假上游模式 |

可通过 `GET /api/health` 查看运行状态，返回字段包含 `ok`、`mock`、`upstream`、`uptimeSec`、`limiter`（窗口占用与冷却剩余秒数）、`sessions`、`cache` 与 `web` 就绪状态。

## 目录结构

```text
relay/                  Go relay 后端（单二进制，仅依赖 gorilla/websocket）
  DEPLOY.md             自部署指南、systemd 与 Caddy/Nginx 反向代理示例
web/                    Nuxt 4 SPA（ssr: false）
  DESIGN.md             前端视觉规范与组件约定
  app/pages/            login.vue（登录）· index.vue（做题工作台）
  app/components/       Sidebar · Statement · EditorPane · OutputPanel · 弹窗与提示组件
  app/composables/      useApi · useQuestionCache · useExamTick · useDebug 等状态逻辑
  app/utils/            Shiki 懒加载高亮 · C++ 补全与签名 · 题面 HTML 清洗
docs/
  ARCHITECTURE.md       架构边界、缓存策略、Relay API 与调试协议详情
```

## 已知边界与注意事项

- **上游单会话制**：AnyView 上游同一账号仅允许一个活跃会话，在 `relay` 登录会顶掉网页端同一账号的登录状态。
- **敏感状态文件**：`relay-state.json` 与 `relay-cache.json` 包含上游 Token 与个人缓存，已在 `.gitignore` 中排除，切勿提交到公开仓库。
- **平台代码覆盖**：上游 `compile` 与 `runGroup` 会直接覆盖平台保存的 `stuCode`，测试重要代码前建议先保存或备份。
- **交互输入限制**：标准输入仅支持单轮 `needInput` 喂入，暂不支持多轮连续交互题。
- **计时范围**：实验题（ES）与已通过题目在服务端不累计 `accumTime`，前端计时仅作参考。
- **单文件模型**：前端按单文件编辑模型实现；`relay` 调试路由已兼容 `lab` / `cd` 变体，但多文件实验与课设暂未接入前端文件管理。

## 更多文档

- 自部署、systemd 与 Caddy/Nginx 反向代理：[relay/DEPLOY.md](relay/DEPLOY.md)
- 架构边界、Relay API、缓存分层与调试协议：[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 前端视觉与组件设计规范：[web/DESIGN.md](web/DESIGN.md)
