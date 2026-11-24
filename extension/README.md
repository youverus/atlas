# Atlas 浏览器扩展

在浏览器侧边栏里使用本机 Atlas 内核：提问、翻译、总结当前页面，工具调用与审批
与桌面端走同一条内核管线。

## 前置条件

- 桌面端需为包含扩展接入的新版本（`go/desktop/`：固定端口 + 扩展配对与
  token 签发）。旧版桌面端没有 `/api/connection` 与扩展准入，连接会失败。
- 桌面端默认监听 `127.0.0.1:7788`（被占用时回退随机端口，以设置页显示为准）。
  也可用环境变量 `ATLAS_DESKTOP_PORT` 覆盖。
- 也可以连接 `atlas -serve`：此时不支持一键配对，扩展 token 与全局 token
  相同（`-auth-token` / `ATLAS_SERVE_TOKEN` / 自动生成，见 `-serve-info` 输出）。

## 构建与加载

```bash
cd go/extension
npm install --ignore-scripts
npm run build        # tsc --noEmit + esbuild，产物在 dist/
```

Chrome / Edge：打开 `chrome://extensions` → 开启「开发者模式」→「加载已解压的
扩展程序」→ 选择 `go/extension/dist` 目录。

## 连接

- **一键配对（推荐）**：扩展选项页点「连接本机 Atlas」→ 在弹出的 Atlas 窗口
  审批卡片选「允许」。扩展 Origin 记入 `~/.atlas/extension.json` 白名单，此后
  免 token 直连，桌面端重启也不必重做。
- **手动 token（备选）**：桌面端「设置 → 浏览器扩展」卡片复制地址与 token，
  在扩展选项页粘贴保存；或直接读取 `~/.atlas/extension.json`。

## 用法

- 点击工具栏 Atlas 图标打开侧边面板；Enter 发送，Shift+Enter 换行。
- **划词浮动条**：在任意网页选中文字，浮出「解释 / 翻译 / 总结」，点击即问。
  浮条只在页面上观察选择事件，不点不发任何数据。
- **右键菜单**：选中文字右键「用 Atlas 解释/翻译/总结所选文字」，或页面空白处
  右键「用 Atlas 总结本页」。
- 「问本页」抓取当前标签页标题与正文（activeTab 授权，仅在你点击后可用），
  随下一条消息一起发给 Atlas。
- 「新会话」为本扩展开一个新的 Atlas 会话，**固定落在桌面端内核的启动目录**
  （正常启动即 home）：会话文件与桌面端、CLI 共用，在该目录下跑 `atlas` 用
  `/resume` 即可打开扩展创建的会话继续。首次创建会把桌面端当前视图切到新
  会话（`POST /api/sessions/new` 的既有语义）；之后的提问按会话键在后台
  运行，不打断桌面端视图。
- 工具审批卡片允许 / 总是允许 / 拒绝，与桌面端同一审批管线。

## 结构

```
src/
├── shared/api.ts        # AtlasClient：REST + 手写 SSE 解析（MV3 无 EventSource）
├── shared/protocol.ts   # background ↔ panel 消息协议
├── shared/settings.ts   # chrome.storage 设置与默认地址
├── shared/log.ts        # 面板/background 共用的会话日志
├── shared/quick.ts      # 快捷动作 prompt 模板
├── shared/render.ts     # 保守 Markdown 渲染（转义优先、白名单恢复结构）
├── background.ts        # service worker：唯一持有 token，SSE 中继与请求转发
├── content.ts           # 划词浮动条（shadow DOM，只观察选择事件）
├── sidepanel.ts / .html / .css
└── options.ts / .html
```

token 只存在于 background 的内存与 `chrome.storage.local` 中；页面内容仅在用户
点击划词条、「问本页」或右键菜单时抽取发送。
