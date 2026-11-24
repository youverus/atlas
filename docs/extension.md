# Extension：浏览器扩展

`go/extension/` 是内核的第四个前端：Chrome MV3 扩展，把同一套回环 HTTP + SSE
数据面接进浏览器——侧边面板会话、划词浮动条、右键菜单快捷动作、整页问答。
它不重新实现任何内核能力：提问走 `POST /api/ask`，事件走 `GET /api/events`，
审批走 `POST /api/approval`，与桌面端是同一条管线、同一个会话文件。

独立 npm 工程（esbuild + TypeScript，**运行时零依赖**），与 `go/web/` 的区别
在于它不是同源页面：扩展从 service worker 跨源 fetch，靠 `host_permissions`
与扩展准入机制获得信任，因此不占 `go/web` 的构建产物，也不动 `httpapi` 的
静态嵌入。

## 1. 连接与凭据

```
扩展 service worker
  │  fetch http://127.0.0.1:7788/api/*，Authorization: Bearer <token>（可选）
  ▼
httpapi guard ── 扩展 Origin 且（token 命中 ExtensionToken ∪ 已配对白名单）→ 放行
  ▼
webhost / assembly（多会话、审批、工具，与桌面端共用）
```

- **一键配对**：扩展选项页点「连接本机 Atlas」→ `POST /api/extension/pair`
  （guard 对未获准扩展只放行这一个入口）→ 服务端挂起并弹出普通审批卡
  （Subject「浏览器扩展请求连接」，Tool 是扩展 Origin）→ 读者在 Atlas 窗口点
  「允许」→ 组合根把该 Origin 记入 `~/.atlas/extension.json` 的 `allowlist`
  （`desktop/extension.go` 的 `extensionAuthorizer`），此后该扩展免 token 直连，
  重启仍有效。`ExtensionApprovals` 是传输层小接口，`atlas -serve` 不注入
  （无窗口可落子），配对路由回 501、token 路径照旧。
- **token（备选）**：桌面端首次启动生成 24 字节 hex 落 `extension.json`，
  跨重启稳定；`atlas -serve` 复用全局 token（`-auth-token` /
  `ATLAS_SERVE_TOKEN` / 自动生成）。`httpapi.Options.ExtensionToken` 是
  token 路径的开关。
- **准入规则**（`httpapi/extension.go`）：跨源请求只有在 Origin 是
  `chrome-extension://` / `moz-extension://` / `safari-web-extension://` 且
  （Bearer 命中 ExtensionToken/AuthToken ∪ Origin 在批准白名单里）时才放行。
  Origin 由浏览器盖章、页面伪造不了；配对卡是唯一把 Origin 写进白名单的门，
  与 channel 网关的配对码同一模式。同一语义下不开启 CORS。
- **端口**：桌面端 `bindServer` 先试 `127.0.0.1:7788`（与扩展默认地址一致，
  `ATLAS_DESKTOP_PORT` 可覆盖），被占用回退 `127.0.0.1:0`。回退后地址以
  桌面端「设置 → 浏览器扩展」卡片显示的为准（数据来自 `GET /api/connection`，
  该端点同时给出 token，供复制）。
- **发现**：`GET /api/connection` 是扩展选项页手动 token 路径的探测目标；
  页面自己拿不到它的响应（无 CORS），能读到的只有已进 guard 的调用方。

## 2. 会话模型

扩展有自己的会话，不与桌面端当前视图混用：

- 会话键存 `chrome.storage.local` 的 `sessionId`（即会话文件路径）。
- 首次提问/快捷动作没有会话时，`POST /api/sessions/new` 开一个，且 **cwd
  固定为内核启动目录**（`/api/state` 的 `directory`，桌面端正常启动即 home）：
  扩展会话落在默认目录分组里，CLI 在同一目录跑 `atlas` 用 `/resume` 即可
  打开并继续，与桌面端看到的是同一份会话文件。这一刻桌面端当前视图会切到
  新会话（既有语义，文档如实记录）；之后所有提问带会话键在后台槽位跑 turn，
  不再打扰视图。
- 桌面端重启后扩展会话的槽位已失：ask 会以 `session is not open` 状态事件
  报错，面板自动 `POST /api/sessions/open` 重开该会话并补发未完成的提问
  （只重试一次，防坏键打转）。
- **历史**：`chrome.storage.local` 的 `log:<sessionId>`，上限 100 条。分工是
  面板写自己的用户条目、background 从事件流落盘 assistant 文本（它看得到每
  一次流，包括划词/菜单触发的提问）；工具卡片与回执是过程信息，不进日志。
  不用 `GET /api/sessions/transcript`，因为它只读「当前视图会话」，扩展会话
  切到后台后读不回来。

## 3. 结构与数据流

| 文件 | 职责 |
|---|---|
| `manifest.json` | MV3；`host_permissions` 仅 127.0.0.1/localhost；content script 注入 http/https |
| `src/background.ts` | service worker：token、SSE 长流（断线退避重连）、REST 转发、右键菜单、quick-ask |
| `src/content.ts` | 划词浮动条：shadow DOM，只观察选择事件，点击才发消息 |
| `src/sidepanel.ts` | 面板：会话管理、流式渲染（rAF 节流）、审批卡片、问本页 |
| `src/options.ts` | 地址 + token，`/api/connection` 连通测试 |
| `src/shared/api.ts` | AtlasClient：REST + 手写 SSE 解析（MV3 无 `EventSource`） |
| `src/shared/log.ts` / `quick.ts` / `render.ts` | 共享日志、prompt 模板、保守 Markdown 渲染 |

一次划词的链路：content script 选中浮条点击 → `quick-ask` 消息到 background →
确保会话 → 记日志 → `POST /api/ask` → 尝试 `chrome.sidePanel.open`（手势来自
点击；不透传时静默降级，提问照常执行）→ 事件流广播回面板渲染。

## 4. 边界与隐私

- 页面内容只在用户点击（浮条/菜单/问本页）时经 `activeTab` 抽取，无自动上报。
- token 只存在于 service worker 内存与 `chrome.storage.local`，不进面板 DOM
  与页面。
- Markdown 渲染转义优先、白名单恢复结构，链接只放行 http(s)。
- 扩展会话与桌面端共用工具审批：面板的允许/总是允许/拒绝与桌面端同一张
  审批表。

## 5. 构建与验收

```bash
cd go/extension
npm install --ignore-scripts
npm run check && npm run build   # tsc --noEmit + esbuild → dist/
```

Chrome `chrome://extensions` → 开发者模式 → 加载已解压的扩展程序 → 选
`go/extension/dist`。端到端验收步骤见扩展目录的 [README](../extension/README.md)。
