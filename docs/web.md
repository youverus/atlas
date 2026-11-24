# Web：窗口前端

`go/web/` 是窗口里跑的那张页面：React 19 + TypeScript + Vite。构建产物提交进 `internal/httpapi/web/dist`，由 `go:embed` 打进 `atlas-desktop`，所以 `go build` 不需要 node。

它只有两个外部依赖方向：一组回环 HTTP 路由，和一条 SSE 流（`/api/events`）。除此之外它不认识这个程序里的任何东西——不知道 provider 是什么，不知道会话文件长什么样，也不知道工具是怎么跑的。这条边界的收益是可测的：同一张页面既能被 Wails 窗口加载，也能被 `vite dev` 在浏览器里改，将来还能由一个 `atlas web` 直接对外提供，要改的只是同源假设（`src/api.ts` 里的相对路径），不是组件。

这份文档回答"它由什么组成、一次点击之后数据怎么走、要改一处东西得动哪几个文件"。**分层为什么这么划**在 [`architecture.md`](architecture.md)，**传输层的规则**（为什么只绑 127.0.0.1、为什么 Host 与 Origin 都要查）写在 `internal/httpapi` 的包注释里，这里只在必要处指过去。

读法：想先建立整体印象读第 2、4、8 节；要改代码先读第 7、9 节再翻第 13 节；接手排查先看第 14 节。

## 1. 它是什么，不是什么

同一个内核有三个前端，窗口是第三个：

| 前端 | 入口 | 渲染 | 数据面 |
|---|---|---|---|
| 终端 | `cmd/atlas` | `internal/tui`（bubbletea） | 进程内，直接读 `app` |
| RPC | `cmd/atlas -rpc` | 无（对端渲染） | stdin/stdout 上的 JSON 行 |
| 窗口 | `desktop/` | `go/web/`（本文件） | 回环 HTTP + SSE |

三个前端跑同一个 `assembly.New`，差别只在表面：同一个配置、同一批适配器、同一套工具、同一道权限门。

它刻意不做的事，每一条都有对应的替代者：

- **不决定"哪些块算一次工具调用"。** `src/lib/fold.ts` 只是内核 `internal/transcript` 折叠的镜像，够画实时流；任何要写回磁盘或重放的东西都读服务端的 `GET /api/sessions/transcript`，角色由 `transcript.Replay` 给。
- **不持有会话。** 会话文件、归档标记、分叉全部在 `internal/app` 与 `internal/session`；页面刷新之后靠 `GET /api/state` 与 `GET /api/sessions/transcript` 把屏幕重建出来。
- **不判权限。** 审批框只是三个按钮，一趟 `POST /api/approval`；决定是 `internal/permission` 的，超时与关闭窗口一律判否也在服务端。
- **不认识模型。** 换模型是一次 `POST /api/models`，页面拿到的是一张服务端拼好的目录（`ModelList`），连"这个模型支不支持推理"都不自己判断。
- **没有路由。** 没有 `react-router`，没有 URL 状态；"现在在看哪段对话"是服务端的事实（`facts.session`）加上侧栏列表里的 `current` 标记，不是地址栏。
- **仅落盘少量 UI 偏好至 localStorage**（如 `atlas.theme`、`atlas.rail.width`、`atlas.side.width`、`atlas.drawer.width`、`atlas.artifact.width`、`atlas.rail.open`、`atlas.terminal.height`）。都不影响对话，删掉只是回到默认。

## 2. 启动链路

从双击图标到第一帧，七步：

1. `desktop/main.go` 解析 flag，`assembly.New` 造出 `Kernel`（配置、模型、会话、`app`、权限门）。权限门拿到的 `Ask` 是一个闭包，里面用的 `server` 变量要到下面才赋值——门要一个能阻塞的地方，server 要门所属的那一轮，两者互为前提；只有第一轮会走进这个环，所以赋值一定发生在第一次提问之前。
2. `webhost.New(kernel)` 把内核的会话与项目翻成传输层要的形状（`Host` 接口），`httpapi.New` 把 `Turn` / `Host` / `Picker` / `Terminal` / `Facts` 装进 `Server`。
3. `server.Listen()` 绑 `127.0.0.1:0`——端口是内核让操作系统给的，不是写死的，因为写死的端口是别人已经占着的端口。地址回给 `desktop`。
4. Wails v3 开一个 1100×760 的原生窗口（`application.WebviewWindowOptions`），`URL` 就是 `server.URL()`。窗口不加载本地文件，也不走 Wails 的 asset server：窗口内容是一块填满它的系统 WebView，指向一个回环地址。
5. `GET /` 落到 `internal/httpapi/web.Handler()`：读内嵌的 `dist/index.html`。任何 `dist` 里不存在的路径也回这份文档（单页应用的自有深链），而不是 404。
6. `index.html` 引 `/assets/index-<hash>.js` 与 `index-<hash>.css`。缓存分两档：`assets/` 下的带 hash 文件 `max-age=31536000, immutable`，文档本身 `no-cache`——重编过的二进制不能端出上周的 HTML。
7. `src/main.tsx` 建根，按 `tokens → shell → chat → panels → markdown` 的顺序装样式表（先词汇、再框架、再框里的东西），挂 `App`。

### 页面由谁画出来

这一点最容易误会：它是 Web UI，但画它的不是浏览器应用，而是操作系统自带的 WebView。

| 平台 | WebView | 本仓库 |
|---|---|---|
| macOS | `WKWebView`（WebKit，Safari 的引擎） | 只打包这个平台——`scripts/package.sh` 拒绝非 Darwin |
| Windows | WebView2（Edge / Chromium） | 未打包 |
| Linux | WebKitGTK | 未打包 |

macOS 那一格不是猜的：Wails v3 的 `pkg/application/webview_window_darwin.m` 里就是 `WKWebView *webView` 与 `didStartProvisionalNavigation` / `didFinishNavigation` 这些 WKWebView 委托。所以窗口里的渲染过程与 Safari 打开同一个地址是同一件事：

```
WebView 发起 GET /            → web.Handler() 读内嵌的 dist/index.html
  ↓ 引用的 /assets/*.js|css    → 同一个 server，同一档缓存
WebKit 解析 HTML/CSS、执行 JS  → React 建 DOM、五张样式表绘制
  ↓ 页面里的 fetch / EventSource
同一源的 /api/*                → 没有 CORS，也没有 base
```

窗口是原生窗口：没有地址栏、标签页、书签。"浏览器"这一层只存在于操作系统里——WKWebView 会在系统侧起 WebKit 自己的网络与渲染子进程，但那是系统框架，不是 Chrome 进程。

**为什么是 `http://127.0.0.1` 而不是 `file://`**

三条理由，缺一条这套结构就得改：

- `EventSource` 与 `fetch` 要同源 http(s)。`file://` 的 origin 是 opaque，SSE 基本连不上。
- 页面与 `/api/*` 同源，所以 `src/api.ts` 里的相对路径不需要 base，服务端也不需要 CORS——只查 `Host` 与 `Origin` 就够。
- 同一张页面能被三种宿主复用：Wails 窗口、`vite dev`（见第 12 节）、将来的 `atlas web`。窗口只是三种之一。

**"没有调用浏览器"是两件事**

- **画界面**：不启动 Chrome，也不要求装 Chrome。用的是系统 WebKit，所以装没装 Chrome 都不影响窗口能不能开。
- **让 agent 上网**：`internal/browser` 确实会 `exec.Command` 拉起真正的 Chrome/Chromium——`launcher.go` 找 `/Applications/Google Chrome.app` 等路径，`cdp.go` 用 DevTools 协议控制。那是 `/api/browser/*`、"浏览器"面板与 `browser_*` 工具的路径，与画界面无关。

区分方法很直接：只开窗口、不碰浏览器面板时，系统里不会多出 Chrome 进程；用 `/browser example.com` 打开过页面之后才会出现。

**一处实际影响**：`vite dev` 里看的是 Chromium，窗口里跑的是 WebKit。这套样式里 `color-mix()` 有 28 处，右下角那组会话导航按钮（`.chat__nav`）还用了 `backdrop-filter`，两个引擎对这类特性的实现并不总是同步。在 Chrome DevTools 里调好的外观，最终要在窗口里复核一遍。

服务端不是"自己的窗口就放行"：`Server.guard` 要求 `Host` 是回环名字、`Origin`（如果有）与 `Host` 一致。公网页面可以 POST 到 127.0.0.1，DNS rebinding 会让浏览器把自己那个域名当 `Host` 发过来，这两个检查就是"我们的窗口"与"任意标签页"的分界。

## 3. 目录与文件

一共约 1.3 万行（不含 `assets/appicon.png`），`src/` 下按"入口 / 契约 / 状态 / 组件 / 样式"五层摆：

### 入口与契约

| 文件 | 行数 | 职责 |
|---|---|---|
| `index.html` | 13 | 唯一的 HTML。`lang="zh"`，`#root`，favicon |
| `vite.config.ts` | 50 | 构建目标 `../internal/httpapi/web/dist`、KaTeX 字体裁剪插件、`/api` 开发代理 |
| `src/main.tsx` | 24 | `createRoot`、样式表装载顺序、StrictMode |
| `src/api.ts` | 567 | **前后端契约的唯一一份**：所有 `interface` 与所有路由调用，外加 `ApiError`（带 `status`，因为 501 与 409 在界面上是三种不同的处理） |
| `tsconfig.json` | — | `strict`、`noUnusedLocals`、`verbatimModuleSyntax`、`noEmit`（类型检查归 `tsc`，构建归 vite） |

### 状态与钩子（`src/lib/`）

| 文件 | 行数 | 职责 |
|---|---|---|
| `fold.ts` | 332 | **折叠层**：`wire.AgentEvent` → `Block[]`，以及重放的 `replay` |
| `artifacts.ts` | — | **产物辅助层**：RFC-4180 CSV 解析与排序、受控 CSP HTML 包装、轮次产物去重、默认导出路径 |
| `commands.ts` | 106 | 斜杠命令表（11 条）、解析、补全匹配、`/help` 的正文 |
| `useStream.ts` | 58 | 全窗口唯一一条 `EventSource`，六个事件名分派 |
| `useStreamRate.ts` | 130 | 3 秒窗口的流式速度估算（只有展示用，轮末收据才是真实用量） |
| `useColumnWidth.ts` | 193 | 可拖的列宽：指针手势、键盘、双击复位、`localStorage` |
| `useTheme.ts` | 41 | 三态主题（system / light / dark），只有 system 跟随系统 |
| `math.ts` | 67 | `remark` 插件：把"$8 / MTok"这种价格还原成文本，别让 KaTeX 吃掉 |

### 组件（`src/components/`）

| 文件 | 行数 | 职责 |
|---|---|---|
| `App.tsx`（在 `src/` 根） | — | 全部状态、全部副作用、所有事件的落点；多设备会话键与远程状态接入 |
| `ControlPanel.tsx` | — | 右侧滑出面板：模型 / MCP / Skill / 插件 / 浏览器 / 远程设备 / 远程控制 / 定时任务 / 通用 tab |
| `DevicesPanel.tsx` / `DeviceDialog.tsx` | — | SSH 远程设备管理卡片、状态与日志查看、`~/.ssh/config` 导入、一键同步模型配置与添加/编辑弹窗 |
| `ModelConfigEditor.tsx` | 970 | 模型配置的图形编辑器（草稿态、校验、保存） |
| `ChatWindow.tsx` | 678 | 阅读列：分轮、过程折叠、自动贴底、复制与分叉、`BlockView` |
| `SessionRail.tsx` | — | 左栏：按 `deviceId:path` 分组展示本地与远程项目、在线/离线状态角标、会话行与归档菜单 |
| `ProjectDialog.tsx` | — | 添加项目模态框：名称 → 设备（本机 / 已配置的 SSH 设备 / 一键新增设备） → 本机文件夹拾取、远端目录浏览器与远端已有项目发现 |
| `Composer.tsx` | 432 | 输入框：自动增高、附件、命令面板、输入法守卫、底部工具条 |
| `EffortControl.tsx` | 311 | 推理强度的滑块 popover |
| `TrajectoryView.tsx` | 245 | 轨迹：同一段会话的"记录视图" |
| `MCPPanel.tsx` | 207 | **未被引用**，被 `ControlPanel` 的 MCP tab 取代 |
| `ContextControl.tsx` | 171 | 上下文占用环 + popover |
| `ForkDialog.tsx` | 146 | 分叉的确认框（命名） |
| `ModelControl.tsx` | 140 | 模型按钮 + 模型列表 popover |
| `PermissionControl.tsx` | 132 | 权限档位按钮 + popover |
| `FileDrawer.tsx` | 119 | 只读文件抽屉（按需读一层目录，与 `ArtifactPanel` 共享右侧抽屉槽位） |
| `ArtifactCard.tsx` | — | 对话内可点击产物卡（按轮去重，独立展示在折叠过程之外） |
| `ArtifactPanel.tsx` | — | 右侧产物预览抽屉（版本历史切换、HTML CSP 沙盒 iframe / SVG / Markdown / CSV / 源码预览、导出工作区、全屏） |
| `SidePanel.tsx` | 112 | 侧对话面板 |
| `SettingsSheet.tsx` | 310 | **未被引用**，被 `ControlPanel` 的设置 tab 取代 |
| `FooterPopover.tsx` | 86 | 输入框下方浮层的共用壳（夹取水平位置、Esc/点外关闭） |
| `TopBar.tsx` | 145 | 顶栏：标题、运行中/已归档/重连中、轨迹开关、底部终端开关与文件栏开关 |
| `TerminalPanel.tsx` | 436 | 底部内嵌终端面板（`@xterm/xterm` + 多标签 + 拖拽高度 + 收起保活） |
| `ApprovalCard.tsx` | 58 | 阻塞一轮的工具审批（允许 / 总是允许 / 拒绝） |
| `ToolCard.tsx` | 57 | 一次工具调用：头是主张，体是参数与结果 |
| `CommandPalette.tsx` | 56 | 输入框上方的命令列表（只补全，不执行） |
| `MarkdownBody.tsx` | 54 | markdown 渲染：GFM + 数学 + 链接走受控浏览器 |
| `ThinkingBlock.tsx` | 20 | 一段思考，默认折起 |

### 样式（`src/styles/`）

| 文件 | 行数 | 管什么 |
|---|---|---|
| `tokens.css` | 312 | 全部颜色/阴影/圆角/间距/字号变量，light 与 `html.dark` 两套 |
| `shell.css` | 746 | 框架：`.app` 网格、侧栏、项目分组、会话行、把手 |
| `chat.css` | 1786 | 阅读列、消息角色、工具卡、输入框、四种 popover、命令面板、轨迹 |
| `panels.css` | 1310 | 侧对话、文件抽屉、设置 sheet、菜单、控制面板、MCP/Skill/插件卡、模型编辑器 |
| `markdown.css` | 156 | 正文排版刻度（标题、列表、引用、表格、代码） |

## 4. 一次提问的完整路径

这是最值得先读懂的一条链路，其余功能都是它的变体。

**发出去（本地的回声）**

1. `Composer` 收到 Enter（`Shift+Enter` 换行；输入法组字期间的 Enter 不算，见第 10 节），把 `text` 与已暂存的附件交给 `App.send`。
2. `App.send` 先问 `commands.parse`：以 `/` 开头且命中表的是命令，走 `runCommand`，不发消息；否则走 `submitTurn`。
3. `submitTurn` 先做三件事，然后才发请求：`fold.prompt` 把读者这句话立刻追加成一个 `user` 块（乐观渲染——等一个来回再显示自己的话，会让人以为没发出去）、把该会话塞进 `runningSessions`（输入框立刻变灰）、清掉上一条 `notice`。
4. `POST /api/ask` 带 `{prompt, session, attachments}`，服务端回 `202` 并立即断开。**这条 POST 不承载任何回答**：回答只从流上来，所以页面拿不到"这一轮的返回值"，也不需要。

**回来（流）**

5. 服务端起跑前发布 `status{running:true, turnId}`，界面用轮次 ID 启用停止按钮。刷新时从 `/api/state` 恢复主轮次 ID，从 `/api/side` 恢复侧轮次 ID。
6. 内核的每个 `wire.AgentEvent` 原样（只加一个 `session`）作为 `agent` 帧推上来：`message_start` / `message_update`（内层还有 `text_start` / `text_delta` / `thinking_start` / …）/ `tool_execution_start` / `tool_execution_update` / `tool_execution_end` / `compaction_start` / `compaction_end` / `agent_end`。
7. `useStream` 按事件名分派到 `App` 的 `onAgent`，`fold.apply` 把这一帧折进对应的块数组，`commitFor(sessionKey, blocks)` 写进 `blocksBySession`。折叠是**按 id 原地更新**而不是重建：流式回答因此是给最后一块追加文字，不是把整段 transcript 重渲染一遍。
8. 回答到屏幕：`ChatWindow` 把块按"每条 `user` 开一轮"切成 turn，再把一轮里的思考、工具、以及过程性回答收进一个可折叠的 `TurnProcess`，只有收尾答案与正文平铺。
9. 跑完时来一帧 `status{running:false, receipt:[…]}`。`onStatus` 做四件事：把会话从 `runningSessions` 里摘掉、`fold.settle` 关掉所有还开着的块、`fold.receipt` 在轮尾落一行"花了多少 / 多快 / 多久 / 几点停的"、然后刷新会话列表与模型/用量（名字可能变了、上下文可能变了、模型可能被切了）。

**失败也走同一条路**

首段回复到达前，界面显示等待状态和已用时间。模型响应流连续 45 秒没有新的有效事件时，Atlas 会关闭该请求并报告流式超时。服务发送的心跳不会延长等待，已返回的正文会保留。

`status.error` 非空时，`fold.notice(…, "error", …)` 在会话里落一条红字，而不是弹窗——错误属于那一轮，读者往回滚的时候应该还看得见它。`/api/ask` 本身的 4xx/5xx（比如 409"这一轮还在跑"）是另一条路：`submitTurn` 的 catch 撤销乐观状态，把消息写进 `notice`，那是页面级提示。

**其余几条路径**

| 动作 | 序列 | 备注 |
|---|---|---|
| 切换会话 | `POST /api/sessions/open` → `GET /api/state` → 需要时 `GET /api/sessions/transcript` → `GET /api/projects` → models/stats/side | 只有该会话在本页生命周期内**产生过实时流**才复用内存里的块（`liveSessionsRef`），否则从磁盘重放——否则会拿旧缓存盖掉别人写的记录 |
| 新建会话 | `POST /api/sessions/new` → state → 清空该键的块 → 刷新列表 | 会话文件要等第一句话才存在，中间态由 `draftDir` 在侧栏画一行"新对话 / 还没有消息" |
| 分叉 | 打开时 `GET /api/sessions/fork-points` → `POST /api/sessions/fork` → state → transcript | 分叉点由服务端算（内核只持有一段会话，路由不收路径参数就是为了不给页面描述另一段会话的机会）；只在一轮跑完、会话未归档时才提供 |
| 归档 / 删除 | `POST /api/sessions/archive`（或 `/delete`） → 若删的是当前会话则重置 → 刷新 | 归档的会话只读：输入框 `locked`，命令白名单只留 `/help` `/model` `/permission` `/mcp` `/plugin` `/browser` 与不带参数的 `/skill` |
| 压缩 | `POST /api/compact`（同步等） | 进度靠流上的 `compaction_start` / `compaction_end`，所以 POST 同步等不会让界面看起来卡住 |
| 侧对话提问 | 需要时 `POST /api/side` → 本地先落一条 prompt → `POST /api/side/ask` | 侧对话有自己的 `sideagent` / `sidestatus` 两条事件，与主对话的块完全分开存 |
| 停止本轮 | 点击停止按钮或按 `Esc` → `POST /api/stop`，携带 `{session, turnId, side}` | 服务端取消对应轮次，收尾后发出 `running:false, aborted:true`。界面保留已生成正文和草稿，显示「已停止本轮」 |
| 审批 | `approval` 帧进 `approvals` → `ApprovalCard` → `POST /api/approval` | 刷新通过 `/api/state` 的 `pending` 重建。回答或取消时，`approval_resolved` 按 ID 撤下审批卡片 |

## 5. 状态模型

`App.tsx` 持有全部状态。它不是"一个 store"，而是三层事实分开摆：

**服务端的事实**（改了就作废，只能重读）

| 状态 | 形状 | 来源 |
|---|---|---|
| `facts` | `State`：version / directory / model / session / running / archived | `/api/state` 与 `hello` 帧 |
| `groups` | `ProjectGroup[]`（项目 → 会话） | `/api/projects` |
| `models` | `ModelList`：目录 + 当前 + 待生效 + 档位 | `/api/models` |
| `permission` | `PermissionState`：当前档位 + 四档的说明 | `/api/permission` |
| `stats` | `Stats`：消息数、token、成本、上下文占用 | `/api/stats` |
| `approvals` | `PendingQuestion[]` | `/api/state` 的 `pending` + `approval` 帧 |

**每段会话各一份的本地状态**

| 状态 | 形状 | 为什么按会话分桶 |
|---|---|---|
| `blocksBySession` | `Record<sessionKey, Block[]>` | 多段会话可以同时跑；一段的回答不能写进另一段的屏幕 |
| `runningSessions` | `string[]`（会话键） | 侧栏每行都要能显示"正在跑"，不只是当前这段 |
| `sideBySession` | `Record<sessionKey, {open, inherited, running, blocks}>` | 侧对话属于它旁边那段主对话，切走再切回来应该还在 |

**页面的临时状态**

`connected`（流连着没有）、`notice`（一行红字）、`draftDir`（刚开始、还没有文件的会话在哪个目录）、`archived`（侧栏在看归档还是活跃）、`controlTab` / `lastExtensionTab`、四个 popover 的开关、`files` / `trajectoryOpen`、`forkPoints` / `forkDraft` / `forkError` / `forkingEntryId`、`trajectory` / loading / error、`theme`。

**会话键**是这三层的接缝，`activeSessionKey = facts.session || 当前行的 path || "__default__"`：

- 内核说过自己是谁（`facts.session`）就用它——新建但还没有文件的会话只有这个来源；
- 否则用列表里标了 `current` 的那一行；
- 都没有（还没写过任何东西）用 `"__default__"` 这个哨兵，保证"空会话的块"也有地方放。

**为什么 ref 与 state 并存**

流是突发性的：一次工具调用可能在一帧里送出几十个 `text_delta`，而这些帧到达时 React 还没提交上一帧的结果。如果折叠只读 `blocksBySession`，第二个 delta 会拿到旧数组，把第一个的成果盖掉（丢字）或者把别的会话的块写回来。

所以每个会变的集合都有一个 ref 与它并行，写入先过 ref：

- `blocksRef` —— 折叠的原地累加与 `commitFor` 的读源；
- `runningRef` —— `onStatus` 判断"刚才是否在跑"要读上一帧的事实；
- `sideRef` —— 侧对话的累加；
- `activeKeyRef` —— 事件分派目标必须是最新活动会话，而不是渲染时闭包里那个；
- `liveSessionsRef` —— 本页生命周期内产生过实时事件的会话集合（见第 4 节"切换会话"）；
- `sideBySession` 的写入口 `updateSideFor` 同样先更 ref 再 `setState`。

代价是这一层不能"只是渲染函数"：改任何一处写入路径，都要同时考虑 ref 与 state 两份。

**不放在 React 里的状态**：滚动是否贴底（`ChatWindow` 的 `pinned` ref）、侧栏哪个菜单开着、附件暂存与预览 URL、popover 的像素位置。它们要么每帧必读（放 state 会让每帧多一次渲染），要么只在 DOM 里有意义。

## 6. 事件契约

`EventSource` 连 `/api/events`。服务端先写一行 `retry: 1000`（断线后一秒重连），然后立刻送一帧 `hello`，之后按事件名分派六种帧：

| 事件名 | 载荷 | 谁处理 | 语义 |
|---|---|---|---|
| `hello` | `Facts` + `session` | `onHello` | 连接建立时的快照；`version` / `directory` / `model` / `session` |
| `status` | `{session?, running, error?, receipt?[]}` | `onStatus` | 一轮的括号。`receipt` 是已经拼好词的字符串数组（"1723 tokens (in 1204, out 519)"、"62 tok/s"、"took 8.4s"、"ended at 15:03:11"），页面只负责排版 |
| `agent` | `wire.AgentEvent` + `session?` | `onAgent` | 内核事件原样转发；页面读的是 wire 的名字 |
| `approval` | `{id, session?, subject, tool}` | `onApproval` | 一个问题推一次；`id` 是回答时要带回去的那个 |
| `sideagent` | 同 `agent` | `onSideAgent` | 侧对话的 agent 事件。名字分开比在每帧里塞一个标记便宜：折叠处不必为每个 case 多判断一次 |
| `sidestatus` | `{session?, open, running, error?, receipt?}` | `onSideStatus` | 侧对话的括号，比 `status` 多一个 `open`——主对话跑一轮不该把侧面板关掉 |

三个约定：

- **重连不带补偿。** `useStream` 里没有重连逻辑：`EventSource` 自己重试，而"断线期间发生了什么"由重连后的 `/api/state`（以及下一帧 `status`）说清。整个页面里唯一需要"事件流断了"这个事实的地方是 `TopBar` 的"重连中"标签，它只在断开时画——一个常亮的"在线"只在熄灭时才有信息。
- **慢读者丢最旧的。** 服务端每个订阅者有 256 帧缓冲，满了就丢最旧的一条而不是阻塞发布。宁可让一个冻住的窗口少一次重绘，也不让整轮对话卡在它身上。
- **心跳是注释。** 空闲时每 15 秒一行 `: ping`，合法 SSE、页面忽略，作用只是尽快发现那个 socket 已经没人读了。

## 7. 折叠层

`src/lib/fold.ts` 是前端唯一"懂事件"的地方。它的输入是 `wire.AgentEvent`，输出是 `Block[]`。

**块有七种角色**，比内核 `internal/transcript` 的六种多一个 `receipt`：

| 角色 | 画成什么 | 何时产生 |
|---|---|---|
| `user` | 右侧气泡（`data-user-message="true"`，导航按钮靠它找位置） | 本地 `fold.prompt`；重放时由服务端给 |
| `answer` | markdown 正文，占满阅读列 | `text_start` / `text_delta`，或重放 |
| `thinking` | 默认折起的一行"已思考" | `thinking_start` / `thinking_delta` |
| `tool` | 一张卡：名字 + 一行结果/耗时，展开是参数与结果 | `tool_execution_start` / `_update` / `_end` |
| `notice` | 两侧带细线的居中一句话（含换行时改块状，因为 `/help` 是一张表） | 压缩、本地命令反馈 |
| `error` | 红字块 | `status.error`、请求失败 |
| `receipt` | 轮尾右对齐的一行小字 | `status.receipt` |

**一个块**（`Block`）：

| 字段 | 用途 |
|---|---|
| `id` | 本地自增，React 的 key，也是原地更新的定位符 |
| `role` | 七种之一 |
| `text` | 正文：回答、思考、通知的文本 |
| `name` | 工具名 |
| `note` | 标记行旁边那一行：参数预览、`done · 1.2s`、`failed` |
| `progress` | 工具还在跑时最后一次进度，落结果后让位 |
| `args` / `result` | 展开后的完整参数与结果（分别截断在 4000 / 20000 字符） |
| `callId` | 工具调用 id（重放时来自条目 id） |
| `open` | 是否还在写。用于 spinner 与"这一块还没结束" |
| `error` | 失败着色 |

**事件 → 块的映射**：

| 事件 | 动作 |
|---|---|
| `message_update` / `text_start` | 关闭所有开着的块（`closeOpen`），新开一块 `answer` |
| `message_update` / `text_delta` | 追加到最后一块**开着的** `answer`；没有就新建一块（宁可多一块，也不丢第一个字） |
| `message_update` / `text_end` | 关掉所有块 |
| `thinking_*` | 同上一组，但角色是 `thinking`；`thinking_end` 只关思考块，不关别的 |
| `tool_execution_start` | 关闭所有块，新开 `tool`，`note` 是压成一行的参数（`trim` 120 字），`args` 是缩进 JSON |
| `tool_execution_update` | 只更新最后一块开着的工具的 `progress`（160 字一行） |
| `tool_execution_end` | 落 `note`（`done` 或 `failed`，加 ` · 1.2s`）、`result`、`error`，并关块；找不到开着的块就补一张已经结束的卡 |
| `compaction_start` | 一条通知："正在压缩上下文（手动 / 接近上限 / 上下文溢出）…" |
| `compaction_end` | 把 `tokensBefore → estimatedTokensAfter` 拼成"上下文已压缩：120,000 → 30,000 tokens"；失败时剥掉内核自己的英文前缀（`Compaction failed: `），因为上面一行已经是中文的"压缩失败：" |
| `agent_end` | 关掉所有块 |

**重放**（`replay`）只做一件事：把服务端给的 `TranscriptBlock[]` 套上本地 id。角色与"一次调用到哪儿为止"由服务端决定——窗口若自己从 wire 形态再推一遍，就是关于"什么算一次工具调用"的第二种意见，而两种意见分叉的那天，会出现两个产品。

**三处已知的偏差**，都是有意留下的：

- 前端多一个 `receipt` 角色，因为它来自 `status` 而不是任何 agent 事件；
- `TranscriptBlock.detail`（内核"只在失败时露"的那一格）在 `replay` 里被丢掉了，`ToolCard` 只画 `args` 与 `result`；
- 会话级的 `notice`（内核关于配置的提醒，`Kernel.Notices`）没有对应的界面入口。

## 8. 组件

```
App
├─ nav.rail
│  ├─ .rail__head            品牌与当前目录名
│  ├─ SessionRail            项目分组 / 会话行 / 归档菜单 / 新建
│  ├─ .rail__extensions      「插件与技能」入口（开 ControlPanel 的上一个 tab）
│  ├─ .rail__foot            设置按钮 + 版本号
│  └─ .resizer--right        左栏宽度把手
└─ main.main
   ├─ TopBar                 标题 / 运行中 / 已归档 / 重连中 / 轨迹 / 产物 / 终端 / 文件
   ├─ .workspace  (1–3 列)
   │  ├─ .chat
   │  │  ├─ ChatWindow  ─┬─ TurnProcess ─┬─ ThinkingBlock
   │  │  │               │               ├─ ToolCard
   │  │  │               │               └─ MarkdownBody（过程性回答）
   │  │  │               ├─ ArtifactCard[]（本轮去重产物卡）
   │  │  │               └─ BlockView    ─┬─ .bubble（user）
   │  │  │                                ├─ MarkdownBody（answer）
   │  │  │                                ├─ ThinkingBlock / ToolCard
   │  │  │                                └─ .notice / .receipt
   │  │  ├─ ApprovalCard[]     还开着的审批
   │  │  ├─ .notice--error    页面级错误（App.notice）
   │  │  └─ Composer
   │  │     ├─ CommandPalette
   │  │     ├─ footer:      ModelButton / PermissionButton
   │  │     ├─ footerRight: ContextRingButton / EffortButton
   │  │     └─ footerOverlay: ModelPopover / PermissionPopover /
   │  │                       ContextPopover / EffortPopover
   │  ├─ SidePanel（开时）      侧对话 + 左边缘把手
   │  └─ FileDrawer / ArtifactPanel（右侧抽屉二选一）
   ├─ ControlPanel（滑出）      设置 / MCP / Skill / 插件 / 浏览器
   ├─ ForkDialog（模态）        分叉命名
   └─ ProjectDialog（模态）     添加项目（名称 / 设备 / 目录）
```

**框架层**

- `TopBar` 只放"窗口现在对着什么"：标题（会话名 → 第一句 → "新对话"）、三个状态 chip、轨迹/终端/文件栏三个开关，其中终端按钮位于右侧文件栏开关左侧，点击在 `.center-pane` 底部展开或收起当前会话目录的内嵌终端。模型与权限不在这里，它们贴在输入框下面——那才是"下一句话用哪个模型"这个问题所在的位置。
- `SessionRail` 按项目分组，因为会话文件自己记着它跑在哪个目录，"这是哪个项目"因此只有一个答案、不需要维护标签。每行是切换按钮，后半截是一个小到不会误触的 `⋯` 菜单（归档/恢复/删除）。项目头上有"＋"（在该项目里新建）与"⋯"（移除项目）；折叠状态存 `atlas.rail.collapsed`。
- `ControlPanel` 是覆盖在 `.main` 上的滑出面板，不是路由：打开它不打断正在跑的一轮。五个 tab 各自在打开时加载自己的数据（`loadSettings` / `loadMCP` / `loadSkills` / `loadPlugins` / `loadBrowser`），面板自己不缓存跨 tab 的事实。
- `FooterPopover` 是输入框四个 popover 的共用壳：向上开、按 anchor 定位、水平夹进父容器、Esc 与点外关闭，`busy` 时 Esc 不生效（正在提交的操作不该被一次误按取消）。

**阅读层**

- `ChatWindow` 的三件事值得记住：**分轮**（每条 `user` 开一轮，开头没有提问的块归第 0 轮）、**过程折叠**（一轮里最后一个"工作块"之前的思考/工具/过程回答合成一个 `TurnProcess`，折叠时只留一行摘要：`3 次思考 · 5 次工具调用 · 1 项失败`，展开时思考块默认展开）、**贴底**（只有读者本来就在底部 48px 以内才跟着新内容滚；往上翻看一句话不会被下一个 token 顶回去）。
- `TrajectoryView` 是同一段会话的第二种读法：一行一条记录，按文件顺序（`seq` 就是行号），不截断、不去掉思考与用量、连压缩与换模型都在。默认全折，因为排查先看形状。
- `MarkdownBody` 的组合是 `remark-gfm`（表格、删除线）+ `remark-math` + `rehype-katex`，中间夹一个自己的 `remarkGuardInlineMath`。**为什么需要它**：单个 `$` 既开公式也开价格，`remark-math` 会把"Pricing: $8 / MTok for input, $4 / MTok for output."变成公式 `8 / MTok for input, `。规则是从 156 条历史回答里量出来的：54 个单美元候选里 26 个真公式、28 个价格，26 个都带 TeX 命令、上标或下标，28 个都不带——所以带这三样之一的才算公式，其余还原成读者打的字。
- 链接不走系统浏览器：`api.browserOpen` 先在受控 Chrome 里开，失败才 `window.open`。

**输入层**

- `Composer` 处理输入框自动增高、命令高亮和输入法组字保护。`@` 候选使用当前会话工作目录的文件列表，按路径逐层浏览文件与目录。选中后只向正文插入路径，包括图片文件。主输入框也提供子代理候选。
- 主对话的拖拽、剪贴板文件或图片和 `+` 选择器共用附件暂存。单条最多 8 个文件，单张图片 5 MiB，单个 PDF 15 MiB，单个文本 512 KiB。图片有缩略图。发送时服务端提取 PDF 文本、读取纯文本或构造多模态图片块。普通文本粘贴进正文。
- `EffortControl` 是一个真正的滑块（停靠点等于模型声明的档位），支持拖动、方向键、Home/End，提交失败会退回服务端的档位。

## 9. 布局与样式

**两根网格，宽度走 CSS 变量。**

外层 `.app` 是两列：`var(--rail-width, 264px) minmax(0, 1fr)`。内层 `.workspace` 按面板开合在 1–3 列之间变：

| 状态 | 列 |
|---|---|
| 都关 | `minmax(0,1fr)` |
| 文件抽屉开 | `minmax(0,1fr) 320px` |
| 侧对话开 | `minmax(0,1fr) var(--side-width, 360px)` |
| 都开 | `minmax(0,1fr) var(--side-width) 320px` |

顺序是"对话 → 侧对话 → 抽屉"，与 DOM 一致：侧对话是同一段会话的另一半，抽屉才是外面那一层。

宽度不内联 `grid-template-columns`：列数会变，从页面拼整条等于把 `panels.css` 那三条规则抄进 TSX，抄出来的第二份迟早与第一份不一样。所以 `App` 只把 `--rail-width` / `--side-width` 写到 `.app` 的 style 上。

**把手**（`useColumnWidth`）：

- 7px 的窄带，绝对定位压在内侧边线上，不进网格——多一列少一列都不用改算术，而它必须活在面板里面（有边的是面板）。
- 手势挂在 `window` 上而不是依赖 `setPointerCapture`：捕获只是优化。实测合成指针输入下 `pointerdown` 在把手、`pointermove` 却落到 `.chat__empty`，只靠捕获会在第一步就断，而光标还停在 `col-resize`。
- 不监听 `window.blur`：窗口本来没聚焦时它在第一帧就送一次，一次正常的拖动会当场断在起点。改成在 `pointermove` 里看 `buttons === 0`——已经松开的指针还在动，说明 `up` 落在了窗口外面。
- 只有落地（抬起 / 取消 / 有键按下）才写 `localStorage`：一次拖动上百个事件，要记住的是最后停在哪一格。
- 键盘：左右键各 16px（反向取决于把手在哪条边），双击回默认。
- `clampTo` 里上限会让位、下限永远赢，中列保底 320px。窗口变窄时按 `resize` 重夹一次——只在拖动那一刻成立的下限等于没有。

**颜色与主题**

`tokens.css` 是一整套 `--fx-*` 变量（名为 `FRAMER-MIRROR` 的生成块），一个 light 段加一个 `html.dark` 段覆盖同一批名字。组件只读变量，不写十六进制。`useTheme` 把 `dark` 类打在 `<html>` 上，三态里只有 `system` 监听 `prefers-color-scheme`——显式选择是一种承诺，机器换主题时不该被推翻。

字号刻度：`--chat-content-max-width: 760px` 是阅读区的上限，阅读列（`.chat__column`）、审批区（`.approvals`）、输入框（`.composer__inner`）与轨迹列（`.trajectory__column`）四者都跟这一条，所以工具卡与正文在同一个左边界上；正文用 `--text-body-lg`，等宽字体给代码与工具输出。

**`data-*` 是状态钩子**，样式读属性而不是读 class 组合：`data-empty`（空会话居中）、`data-drawer` / `data-side`（列数）、`data-current`（当前行）、`data-active`（激活的按钮/tab）、`data-open`（折叠区）、`data-error`、`data-locked`（归档只读）、`data-kind`（轨迹行类别）、`data-peak`（滑块在峰值档）。这样"状态"与"长相"只有一处连接，改外观不必改 TSX。

**遗留的无效样式**（改样式时会撞见，别以为是自己在用的）：

- `tokens.css` 末尾的 `.chat-reading` / `html[data-chat-font-size=…]` / `html[data-chat-layout="wide"]` / `.chat-content-column` 没有任何组件使用，也没有代码设置那两个 `data-` 属性——那是一次"字号与宽版偏好"的残留，界面上没有入口。
- `markdown.css` 用 `calc(… * var(--type-scale, 1))`，`--type-scale` 只由上面那段无效样式设置，实际恒为 1。
- `MCPPanel.tsx`、`SettingsSheet.tsx` 两个组件文件没有任何引用（`App` 与 `ControlPanel` 都不导入）。

## 10. 交互细节

| 场景 | 行为 | 为什么 |
|---|---|---|
| Enter / Shift+Enter | 发送 / 换行 | 输入框里唯一需要的两档 |
| 中文输入法 | 组字中的 Enter 不发送（`isComposing`、`keyCode 229`、以及 `compositionend` 后同一 tick 的 Enter 都放过） | 选词用的 Enter 被当成发送，是最伤人的一个 bug |
| 空会话 | 输入框竖直居中（`data-empty`），写了第一句就回到底部 | 打开一张白纸时重心在中间；有内容后重心在上面那段对话 |
| 往上翻看历史 | 不再贴底，右下角三个按钮：回到开头 / 上一条提问 / 回到最新 | 新 token 把读者顶回去，是流式界面最常用的失效方式 |
| 复制 | 复制这一轮的**回答**（不含工具、思考、通知、提问），异步剪贴板失败时退回 `textarea + execCommand` | webview 可能把它当成不安全上下文，`navigator.clipboard` 直接不存在 |
| 分叉 | 每条回答下面一个入口（内核只为收尾答案列切点）；整段复制走 `/fork` | 只在一轮跑完、会话未归档时提供——一轮在跑时分支会成为同一个文件的两个写者 |
| 归档只读 | 输入框 `locked` 并换 placeholder；命令只放行白名单 | 内核本身拒绝写归档会话，界面不应先把按钮给出来 |
| 命令面板 | `/` 开头且还没空格时出现；点一行只把名字填进输入框，不执行；Esc 关掉 | 与终端同一套行为：补全一行，而不是发射它 |
| 路径引用 | `@` 补全文件或目录，选中后插入路径 | Agent 可按需读取项目资料 |
| 附件 | 拖拽、粘贴文件或 `+` 选择后显示缩略图或类型、大小与移除按钮 | 可在发送前检查所附文件 |
| 审批 | 三键：允许 / 总是允许 / 拒绝；"总是"是独立按钮而不是复选框 | 决定是"这一次怎么答"，不是"给这个答案加个修饰" |
| 侧对话 | 底部输入框只认 `/btw` 与 `/side off`，其他命令回一句"在侧对话里不跑" | 两个对话同时可见，其余命令没有"在哪边跑"这个问题 |
| 轨迹 | 只在打开时加载，换会话或一轮结束后重取；跑着的时候不取 | 一轮在写，取回来的是写了一半的记录 |

## 11. 接口清单

`src/api.ts` 是唯一出入口。所有请求都是同源相对路径，所以 `fetch` 不需要 base，也不需要 CORS。

| 方法 | 路由 | 调用点 |
|---|---|---|
| `state` | `GET /api/state` | 挂载、`syncConfig`、切/建/分叉/恢复会话 |
| `ask` | `POST /api/ask` | `submitTurn`（202，无响应体） |
| `stop` | `POST /api/stop` | 取消指定轮次（202 表示已接受取消，终态由 SSE 发布） |
| `approve` | `POST /api/approval` | `ApprovalCard` |
| `sessions` | `GET /api/sessions` | **无调用点**：列表统一走 `projects` |
| `openSession` | `POST /api/sessions/open` | `switchSession` |
| `newSession` | `POST /api/sessions/new` | `newSession`（可带 `cwd`） |
| `transcript` | `GET /api/sessions/transcript` | `loadTranscriptFor`、分叉后 |
| `trajectory` | `GET /api/sessions/trajectory` | `loadTrajectory` |
| `forkPoints` | `GET /api/sessions/fork-points` | `forkable` 变化时的 effect |
| `forkSession` | `POST /api/sessions/fork` | `forkSession` |
| `archiveSession` / `restoreSession` / `deleteSession` | `POST /api/sessions/{archive,restore,delete}` | `SessionRail` 的回调 |
| `projects` | `GET /api/projects?archived=` | `refreshSessions` |
| `addProject` / `removeProject` | `POST /api/projects` / `…/remove` | `ProjectDialog` 提交（`{path, name?}`）与侧栏移除 |
| `pickFolder` | `POST /api/projects/pick` | `ProjectDialog` / `SchedulePanel`（501 = 这个构建没有选择器，弹窗内自动切到手填路径） |
| `createTerminal` / `writeTerminal` / `resizeTerminal` / `closeTerminal` + `/api/terminal/stream` | `POST /api/terminal/{create,write,resize,close}`、`GET /api/terminal/stream` | `TerminalPanel`（按当前会话目录分配 PTY 与 SSE 流；501 = 非桌面/无 PTY 宿主） |
| `compact` | `POST /api/compact` | `/compact` |
| `side` / `openSide` / `sideAsk` / `closeSide` | `GET`/`POST /api/side`、`POST /api/side/ask`、`POST /api/side/close` | 侧对话 |
| `models` / `setModel` / `setThinking` | `GET`/`POST /api/models`、`POST /api/thinking` | `refreshModels`、底部 popover、设置页 |
| `permission` / `setPermission` | `GET`/`POST /api/permission` | 权限 popover、设置页 |
| `reloadConfig` | `POST /api/config/reload` | 设置页确认 |
| `modelConfig` / `saveModelConfig` | `GET`/`POST /api/config/models` | `ModelConfigEditor` |
| `mcp` / `refreshMCP` | `GET /api/mcp`、`POST /api/mcp/refresh` | MCP tab |
| `skills` / `skillContent` / `loadSkillPrompt` | `GET /api/skills`、`GET /api/skills/content`、`POST /api/skills/load` | Skill tab、`/skill` |
| `plugins` | `GET /api/plugins` | 插件 tab |
| `browser*` | `GET /api/browser`、`POST /browser/{open,navigate,action}`、`GET /browser/screenshot` | 浏览器 tab、`/browser`、`MarkdownBody` 的链接 |
| `stats` | `GET /api/stats` | 上下文环、轮末刷新 |
| `files` / `file` | `GET /api/files?path=`、`GET /api/file?path=` | `FileDrawer` |
| `artifacts` / `artifact` / `deleteArtifact` / `exportArtifact` | `GET /api/artifacts`、`GET/DELETE /api/artifacts/{id}`、`POST /api/artifacts/{id}/export` | `ArtifactPanel`、`ArtifactCard` 与实时工具事件同步（501 = 宿主未注入产物控制器） |

服务端的实现分布在 `internal/httpapi/httpapi.go`（会话那一组）、`host.go`（窗口外壳那一组）、`side.go`、`approval.go`、`models_config.go`，并由 `internal/webhost` 接到内核。路由表见 `internal/httpapi/host.go` 的 `mountHost`。

一条契约层面的约定值得抄下来：**回答"屏幕上那段会话"的路由不吃 session 参数**。`/api/sessions/transcript`、`…/trajectory`、`…/fork-points` 都不带路径，因为它们回答的永远是"已经切过去的那一段"；带参数就等于允许页面描述另一段会话，而内核只持有一个当前会话。写新路由时别破坏这条。

## 12. 构建、开发与验证

**构建**

```bash
cd go/web
npm install       # 锁文件就在 web/ 下，与仓库根的 npm 工程是两套
npm run build     # tsc --noEmit && vite build
```

`vite.config.ts` 把产物直接写进 `../internal/httpapi/web/dist`（`emptyOutDir: true`），所以"构建前端"与"更新要提交的产物"是同一步。`npm run build` 先跑 `tsc --noEmit`：类型错误不会等到运行时才发现。产物是**单块**（当前约 777KB JS + 99KB CSS）——KaTeX 的 278KB 故意不打散，因为任何含公式的回答都需要它，拆出去只会让第一个公式比周围的文字晚一帧到达。

`katexWoff2Only` 插件在 `vite` 处理 CSS 之前删掉 KaTeX 字体声明里的 `woff` 与 `ttf` 两档：三种格式共 20 个字族，实际只用到 woff2，另外两档是 876KB 会进 dist、进二进制、没人下载的文件。

`dist` **提交进仓库**，`internal/httpapi/web/web.go` 用 `//go:embed all:dist` 装进来。这样 `go build` 不需要 node，缺 `dist` 是编译错误而不是一个空白窗口。代价是改前端必须重新构建并提交产物，否则二进制里是旧的页面——打包脚本 `scripts/package.sh --web` 会先跑一次 `npm run build`，就是为这件事。

**开发**

`npm run dev` 起 `vite`，`/api` 被代理到 `http://127.0.0.1:7788`。**这个端口目前对不上**：`desktop` 用 `server.Listen()` 绑的是 `127.0.0.1:0`（操作系统给的临时端口），没有 `-port` 参数，所以默认情况下代理目标上没有人监听。要在浏览器里改前端，现在的可行做法是：把 `vite.config.ts` 里的 target 改成窗口实际用的那个端口（启动时可以从窗口地址栏或 `lsof -iTCP -sTCP:LISTEN -P | grep atlas` 读出来），或者先给 `desktop` 加一个固定端口的 flag。这不是前端的问题，是开发链路欠的一步。

**验证**

- 传输层与宿主：`go test ./internal/httpapi/... ./internal/webhost/...`，模块闸门 `tests/httpapi/run.sh`、`tests/webhost/run.sh`。
- 前端本身**没有单元测试**（`web/src` 下没有 `*.test.*`，`tests/` 里也没有跑 npm 的脚本）。能自动验证的是"页面产物能构建、类型过关"，交互靠手工：`bash tests/run.sh` 里没有任何一项会打开这个页面。
- 窗口本体：`tests/desktop/run.sh` 只做 `gofmt` / `go vet` / 链接，没有测试步骤——它的注释写明了原因（有意思的行为都在 `internal/httpapi`）。手工构建窗口要在 `desktop/` 里单独 `go build`（它是独立 module，要 cgo 与平台 WebView，内核的 `CGO_ENABLED=0` 交叉编译路径碰不到它）。
- 手工自检清单（改完前端至少跑一遍）：空会话居中 → 发一句 → 流式回答 → 工具卡展开 → 审批三键 → 换会话 → 归档后只读 → 分叉 → 轨迹 → 侧对话 → 设置里换模型 → 深色主题 → 拖两根列宽并刷新。

## 13. 扩展指南

**加一种块角色**

1. `src/lib/fold.ts` 的 `Role` 联合类型加一项，`apply` 里给出产生它的分支；
2. `src/components/ChatWindow.tsx` 的 `BlockView` 加一个 case（`default` 已经兜底，但会画成通知，别指望它）；
3. 若内核也要产出它：`internal/transcript` 的 `Role` 与 `String()`、`internal/wire` 的事件形状，以及 `TranscriptBlock` 的翻译（`internal/webhost/transcript.go`）；
4. 样式写在 `chat.css` 的对应小节里，用既有 token。

**加一条斜杠命令**

1. `src/lib/commands.ts` 的 `COMMANDS` 表加一行（`name` / `usage` / `summary`）——`/help` 的正文与命令面板都从这张表读；
2. `src/App.tsx` 的 `runCommand` 加一个 case（不认识的命令会落到"窗口没有这个命令"，表里有而 switch 没有会静默什么都不做）；
3. 若命令要改会话：把名字加进归档白名单（`runCommand` 开头那串 `!==` 判断）；
4. 若命令由服务端执行：`src/api.ts` 加方法，`internal/httpapi` 加路由，`internal/webhost` 加实现。

**加一条 SSE 事件**

1. 服务端 `internal/httpapi` 里 `broker.publish(streamEvent{Name: "...", Data: ...})`；
2. `src/lib/useStream.ts` 的 `StreamHandlers` 加回调、`addEventListener` 加一行；
3. `src/api.ts` 加载荷类型；
4. `src/App.tsx` 的 `useStream({...})` 里给出落点。

**加一个控制面板 tab**

1. `src/components/ControlPanel.tsx` 的 `ControlTab` 联合类型加一项；
2. tab 按钮数组加一个（带 `badge` 的话算一下计数）；
3. `load*` 一个加载函数，并接进那个按 `tab` 分派的 `useEffect`；
4. 渲染分支加一段；
5. 若要能被 `/命令` 打开：`App.openControlTab` 的调用点 + `lastExtensionTab` 的类型（它是 `Exclude<ControlTab,"settings">`，自动跟着变）。

**改布局列**

只动 `panels.css` 的 `.workspace[data-*]` 三条规则与 `shell.css` 的 `.app`；宽度数值通过 `App` 的 `shellStyle` 传，不要在内联 style 里拼 `grid-template-columns`。

## 14. 已知边界与遗留

按"会不会咬到人"排：

1. **开发代理端口对不上**（第 12 节）：`vite dev` 默认代理到一个没人监听的端口。改前端目前要么临时改配置，要么重建二进制。
2. **前端没有自动化测试**：`fold.ts`、`commands.ts`、`useColumnWidth` 的 `clampTo` 都是纯函数或接近纯函数，是最容易补、也最值得补的三处；现在它们只靠手工点。
3. **两个死组件**：`MCPPanel.tsx`、`SettingsSheet.tsx` 没有引用，功能已并入 `ControlPanel`。留着会让"改设置页"改到一份不生效的文件。
4. **一段死样式**：`tokens.css` 末尾的字号/宽版偏好（`.chat-reading`、`data-chat-font-size`、`data-chat-layout`、`.chat-content-column`），界面上没有入口，`--type-scale` 恒为 1。
5. **`api.sessions` 没有调用点**：会话列表统一走 `/api/projects`（因为侧栏按项目分组）。它留着是同一条路由的第二种读法，谁先用它谁先制造"两份列表"。
6. **`TranscriptBlock.detail` 在重放时被丢掉**：工具卡只画参数与结果，内核"只在失败时露"的那一格到不了屏幕。
7. **侧对话只在内存里**：关掉就没了（`/side off` 的语义），刷新页面也回不来——`sideBySession` 只活在这一次页面生命周期里。
8. **没有 URL 状态**：刷新回到内核当前会话，不能"把某段对话的地址发给别人"。这是单窗口应用的取舍，不是缺陷，但会挡住"从链接打开"这类需求。
9. **轨迹与阅读列是两次独立读取**：`/api/sessions/trajectory` 直接读文件，`/api/sessions/transcript` 读内核内存镜像。同一段会话在极端情况下（一轮刚结束、文件与内存还没对齐）可能差一条，所以轨迹只在轮末重取。
10. **`--fx-*` 变量是生成产物**：`tokens.css` 里 `FRAMER-MIRROR` 两个标记之间的内容按标记的说法是由生成器写的，仓库里没有那个脚本，所以改之前先确认来源——直接手改会在下一次生成时被盖掉，那是这个文件里唯一可能"今天改了、明天没了"的一层。
11. **远程设备与远程项目（后续规划）**：`ProjectDialog` 参照 `zcode` 的 `ProjectEditor` 保留了「项目名称 → 设备 → 项目目录」三段结构，其中「设备」下拉目前仅启用「此电脑（本地）」，「远程设备（规划中）」作为不可选占位项展示；远端设备配置、连接状态、远端目录浏览、以及远端已有项目的添加均留作后续迭代实现，当前 `projects.json` 与 `/api/projects` 仅处理本地目录与可选展示名称。
