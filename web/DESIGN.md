# DESIGN.md — AnyView Next 视觉系统

精密暗色工作台。深度靠**亮度分层 + 发丝线**表达，不靠阴影；全站只有一个彩色意图（靛蓝 = 可交互/聚焦/选中），绿红琥珀只做语义状态色。身份由 mono 数据字体与 hairline 精度承担，不由装饰承担。

## 1. 气质
- 扁平：仅浮动层（modal/toast/下拉）允许阴影，其余一切靠底色阶差与 1px 线
- 克制：靛蓝只出现在 focus ring、选中、拖拽手柄、链接；绝不装饰
- 干净：无底色药丸 chip，状态用「小圆点/纯色文字」表达；无 · 分隔符
- 现代：系统 UI 字体栈（产品权限），Fira Code 承担一切数据/文件名/计时（400/500/600 三档随包自载）

## 2. 色彩

### 暗色（默认）
| Token | 值 | 角色 |
|---|---|---|
| `--bg-canvas` | `#0b0c0e` | 最深底：编辑器、输出、页面 |
| `--bg-surface` | `#101114` | 第二中性层：侧栏、工具栏、面板头、modal |
| `--bg-elevated` | `#17191e` | 悬停/选中行、输入框、分段控件底 |
| `--border-subtle` | `rgba(255,255,255,0.05)` | 默认发丝线 |
| `--border` | `rgba(255,255,255,0.09)` | 强一档的线/输入框描边 |
| `--text-primary` | `#f2f3f5` | 主文字（非纯白） |
| `--text-secondary` | `#b0b5bf` | 次级 |
| `--text-muted` | `#7e8390` | 弱化（对 canvas ≥4.5:1） |
| `--text-faint` | `#5c606b` | 最弱（仅禁用/装饰性） |
| `--accent` | `#7170ff` | 唯一彩色意图（hover `#8b8aff`） |
| `--ok` | `#3fb950` | 通过/成功语义 |
| `--warn` | `#d29922` | 计时中/等待输入/调试暂停 |
| `--err` | `#e5534b` | 编译错/断点/失败语义 |

### 浅色
canvas `#f6f7f8` · surface `#ffffff` · elevated `#eef0f2` · border `rgba(9,10,12,0.08)`/`0.13` · text `#1b1d21`/`#5d626c`/`#6d727d` · accent `#5e6ad2` · ok `#1a7f37` · warn `#9a6700` · err `#cf222e`

### 用色纪律
- 语义色只允许：文字状态、4px 状态点、lint 下划线、断点。禁止大底色块 + 彩字组合的 chip
- 靛蓝禁止用于：状态指示（那是语义色的领地）、装饰、标题
- 彩底灰字禁止；暗底正文永远不是纯白

## 3. 字体
- 栈：`-apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif`；mono：`"Fira Code", ui-monospace, "SF Mono", Menlo`（英文代码）；中文回落到系统等宽/雅黑
- 固定刻度（产品 UI，无流式）：11 微标签 / 12 UI 默认 / 13 正文与数据 / 14.5 题面正文 / 15 代码默认与面板标题 / 19 题面标题
- 字重三档：400 读、500 UI 强调（workhorse）、600 宣告（bold 700 只留给 brand）
- 字距：≥15px 标题 `-0.01em`，其余 normal；mono 一律 `tabular-nums`
- 行高：UI 1.5、题面 1.75、代码 1.65

## 4. 组件词汇表（全站唯一）

**按钮**（只有这一套）
- `.btn`：12px/500，radius 6px，padding 4px 10px
- `.btn-primary`：中性反色填充（暗底白填充/亮底黑填充）——唯一允许的填充按钮，仅登录/弹窗确认/完成等**模态主动作**；工作台工具栏一律不用（实底填充在常驻工具栏上过于抢眼，运行按钮亦为 ghost，2026-09-16 用户定夺）
- `.btn-ghost`：透明底 text-secondary，hover `--bg-elevated` + text-primary——工具栏、顶栏、卡片动作（含「运行」）
- `.btn-icon`：24×24 ghost 方体，内嵌 16px SVG
- 状态五件套必齐：hover / focus-visible(2px 靛蓝 ring) / active / disabled(0.4) / loading(文字替换)

**输入**：`--bg-canvas` 内嵌底 + `--border` 描边 + radius 6 + focus 靛蓝描边；高度 26–34px

**分段控件**：`--bg-canvas` 槽 + radius 6；激活项 `--bg-elevated` + text-primary，无阴影

**面板头/工具栏**：统一 `--bg-surface` + 底部 `--border-subtle`；高度阶梯 44(顶栏)/36(工作台)/32(面板)

**圆角**：控件 6 / 浮层 10 / 全站无 999px 药丸（调试会话标签等一律改为「点 + 文字」）；卡内嵌小件（徽标/芯片/行内码）允许 2–5px 更小半径，隶属宿主刻度

**图标**：lucide-vue-next 一套（24 viewBox 基准），尺寸档 10–15px；光学线宽恒定 ≈1.2px，由 main.css `.icon` 规则按 width 档补偿（模板 `:stroke-width` 不生效，勿再调）。禁 unicode 字形（▶◐vpn窗外）。终端提示符 `›` 属内容不算图标。例外：编辑器 gutter 断点/调试行标记走 innerHTML 手绘（CodeMirror GutterMarker 挂不了 Vue 组件），16 viewBox fill 实现

**浮层**：backdrop `rgba(0,0,0,0.55)`；modal `--bg-surface` + hairline + radius 10 + `0 8px 24px rgba(0,0,0,.4)`（全站唯一阴影）；toast 同族

## 5. 布局
- 8px 基准；面板内边距 12/16；栏间 hairline 分隔，resizer 命中区 7px、可见线 1px（hover/拖拽变靛蓝）
- 题面正文 max-width 72ch；标题上方留白 > 下方
- 编辑器/输出底 = canvas，侧栏/工具栏 = surface，两层中性即全部层级语言

## 6. 禁止清单（本项目特化）
- unicode 当图标（▶ ◐ ◫ ▾ ⛶ ■ ✓ ✗ ↗ ↷ ❐ ⬒ ▴ ✕ ☰）
- 彩底 chip/药丸（已通过、调试中、诊断计数）
- >1px 彩色左边框 callout
- ·字符分隔符（顶栏、筛选行）
- 除浮层外一切 box-shadow
- 彩色渐变、玻璃拟态、动效入场编排

## 7. Do
- 状态用「4px 点 + 语义色文字」；计数用 mono
- hover 只动底色/文字色，120ms；无位移动画；呼吸点/闪烁点等循环动效在 prefers-reduced-motion 下降级为静态
- 代码块/文件名/编号一律 mono
- 空状态写清下一步动作（Ctrl+Enter 提示保留）
- 断点红点 8px、调试行 2px 琥珀内嵌线——调试语义保持高辨识
- 默认图标优先；「运行」是唯一常驻字按钮
- 计时不上屏；exam tick 后台静默
- 可配：编辑器字号/缩进/折行、题面字号、分栏宽高与停靠（可重置）
