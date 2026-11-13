# 架构与协议说明

本文记录 `anyview-next` 的前后端职责边界、缓存策略、Relay API、在线调试协议镜像以及已知状态语义。

## 职责边界

- **会话与 XFF 收敛**：AnyView 网关将登录态绑定在 `X-Forwarded-For` 上。`relay` 在登录（RSA 加密 `${password},${timestamp}`）和所有后续上游请求中注入同一 `X-Forwarded-For` 与 `User-Agent`，浏览器只持有不透明的 `an_sid` HttpOnly Cookie，不直接接触上游 Token。
- **有状态服务下沉**：`exam` 计时依赖常驻 WebSocket，这类需要心跳与断线重连的连接由 `relay` 持有；编辑器状态、题面缓存索引、本地草稿等 UI 状态归属 `web`。`relay` 不内置本地编译器或调试器进程，不派生 `g++` / `gdb`。
- **真实编译器诊断**：代码诊断直接解析上游 `compile` 返回的 GCC 输出文本并映射为编辑器标注，不引入本地 `clangd`；编辑器补全由 `@codemirror/lang-cpp` 与轻量片段/函数签名表提供。
- **限速与 WAF 防封漏斗**：所有上游 HTTP 请求经过 `relay/limiter.go` 的滚动 60s 滑动窗口限速器，按 `60s / rpm` 拉开相邻请求间隔。一旦响应命中 WAF 拦截页特征（`403` 拦截页或 `429`），立即进入全局静默冷却，冷却期内直接返回 `429` 而不继续请求上游，防止封禁期被续期。
- **离线 Mock**：`-mock` 模式在内存中模拟目录、题面、模板、编译判题与 GDB 调试会话，不读写真实 `state` / `cache` 文件，也不触碰真实上游。

## Relay API

| 端点 | 说明 |
| --- | --- |
| `POST /api/auth/login` | 接收 `{username, password}`，拉取上游 RSA 公钥加密并登录，签发 `an_sid` Cookie 并持久化会话 |
| `GET /api/auth/status` | 返回当前浏览器会话的登录态、学号、姓名与 Token 过期时间 |
| `POST /api/auth/logout` | 停止当前会话的 exam 计时、断开 WebSocket 并清理会话 |
| `ANY /api/up/<service/...>` | 通用上游透传（自动注入 `Token` / `X-Forwarded-For` / `User-Agent`）；特定读端点走缓存池 |
| `POST /api/exam/tick` | `{eid}` 启动或切换计时；`{eid: null}` 停止计时，心跳（40s `type:117`）与重连由 `relay` 维护 |
| `GET /api/exam/status` | 返回 `{active, running, tickedAt, connected, kicked, lastError?}` |
| `POST /api/work/run` | `{eid, qfn, code, language, kind, input?}`：编译 → 轮询 `runGroup` 直到 `end=true` → 必要时通过 `exeInput` 喂入标准输入，并返回结构化 GCC 诊断 |
| `POST /api/work/save` | 同步代码到平台 `stuCode` 存档并写穿本地缓存 |
| `ANY /api/gdb/{oj\|lab\|cd}/<code\|debug\|exercise>/...` | `gdb-service` 协议镜像（详见下文） |
| `GET /api/health` | 返回服务运行状态、限速窗口、WAF 冷却剩余时间、会话数、缓存统计及 `-web` 就绪状态 |

### 缓存池策略（`relay/cache.go`）

前端高频读取的三个 `GET /api/up/scheme-service/*` 端点按不同策略缓存并落盘至 `-cacheFile`：

| 端点 | 策略 | 隔离作用域 |
| --- | --- | --- |
| `scheme/list/student/*` | 命中即秒回；附带 `X-Relay-Stale: 1` 触发后台校验与前端补拉刷新 | **混合**：剥离 `stuCode` 后的题面跨账号共享，个人代码按账号隔离拼回 |
| `scheme/getQuestionTemplate/*` | fresh 期 30 天，期内直接命中缓存 | **公共**：初始模板跨账号共享 |
| `catalog/list` | **网络优先**：正常直连上游以获取最新 `pass` 与 `accumTime`；仅在上游不可达或 `5xx` 时回退缓存 | **个人**：按账号严格隔离 |

- **写穿与淘汰**：`save` 与 `compile` 成功后直接写穿当前账号的 `stuCode`；判题通过后同步更新本地兜底 `catalog` 条目的 `pass` 状态。后台校验失败时进入 30s 退避，避免连续请求故障上游；容量超限（默认 600 条 / 64 MiB）时按 `fetchedAt` 淘汰。

## GDB 调试协议镜像

`relay` 不自造调试协议，通过通用路由镜像原版前端对 `gdb-service` 的请求格式：

```text
ANY /api/gdb/{oj|lab|cd}/<code|debug|exercise>/<...>
```

- **变体映射**：`oj` 映射至 `/gdb-service/<path>`；`lab` 映射至 `/gdb-service/lab/<path>`；`cd`（或 `courseDesign`）映射至 `/gdb-service/courseDesign/<path>`，并自动补充 `courseDesignExerciseId = labExerciseId`。
- **载荷编码**：默认按原版 `fn()` 规则将 JSON 拍平为 `multipart/form-data`（嵌套对象为 `a.b`，数组为 `a.[0]`，空对象/空数组为 `{}`/`[]`）；携带 `?mode=json` 时直接转发 JSON（用于 `lab` 变体中的部分 JSON 端点）。
- **请求头与透传**：自动提取 body 或 query 中的标量 `eID` 作为 `Eid` 请求头；上游响应原样返回给前端。

### 常用调试载荷形状

| 操作 | 载荷内容 |
| --- | --- |
| OJ 启动调试 (`debug/start`) | `{bp: "1,5,7", questionFullName, eID, isTeacher}`（`bp` 为逗号分隔行号；Python 题 `language=3` 时附带 `stuCode`） |
| 步过 / 步入 / 继续 (`debug/nextStep`) | `{cmd: "next\n" \| "step\n" \| "continue\n", eID, isTeacher}` |
| 增删断点 (`PUT /debug/breakPoints`) | `{addPoints \| delPoints: <行号>, eID, isTeacher}` |
| 观察点 (`debug/setWatchPoint`) | `{addPoints \| delPoints: <变量名>, eID, isTeacher}` |
| 调试中输入 (`code/exeInput`) | `{inputStr, isTeacher, language, kind, eID}` |
| lab 启动 (`debug/start`) | JSON `{questionFullName, labExerciseId, breakPointInfo: [{path, codeContent, breakPoints}], isTeacher}` |
| lab 步进 / 跳出 / 停止 | 表单 `{cmd?, labExerciseId, isTeacher}` |
| 前置编译 | 必须先执行 `compile(isDebug: true)` 且编译通过后才能启动调试 |

说明：OJ 变体（`oj`）对普通学生账号可用，返回 `RunData` 包含 `output`、`lineNum`、`end`、`needInput`、`backtrace[]`、`variables[].variate`（含递归 `innerObj`）等字段；`lab` 变体在上游对本科生账号会返回 `401` 权限不足。

### 前端调试协同与 CodeMirror 要点

- **断点与快捷键**：断点按题保存在 `localStorage`（`an.bp.<scheme>.<eid>`），开启调试前至少需设置一个断点。调试控制快捷键：`F8` 开始/停止、`F9` 继续、`F10` 步过、`F11` 步入、`Shift+F11` 跳出；`end=true`、切题或关页（`sendBeacon`）时自动退出调试会话。
- **CodeMirror 6 gutter 实现约束**：
  1. `ViewUpdate` 没有 `effects` 字段，`lineMarkerChange` 应比较 `startState.field(f) !== state.field(f)`。
  2. gutter 回调接收的 `line` 为 `BlockInfo`（含 `from`，无 `number`），需通过 `doc.lineAt(line.from).number` 换算行号。

## 前端状态与上游协议细节

- **多标签页计时协调**：通过 Web Locks（`an.exam-leader`）选举主标签页，由主标签页发送 `exam/tick` 纠偏计时与关页停表；用户切换焦点或切题时通过 `an.exam.intent` 广播最新目标。不支持 Web Locks 的浏览器将退化为各自直接上报。
- **草稿与云端存档语义**：`scheme/list/student` 返回的 `stuCode` 是平台上**上次保存的代码**（提交编译或运行也会覆盖该存档），而非题目初始模板；「重置模板」从 `getQuestionTemplate` 获取原始模板。前端本地草稿防抖保存在 `localStorage`，读取时优先于云端存档。
- **上游信封状态码**：上游 JSON 信封 `{code, message, data}` 中，除 `200` 和 `0` 外，`31`、`41`、`70` 也属于历史兼容成功码，`relay` 缓存与前端均将其视为正常响应处理。
