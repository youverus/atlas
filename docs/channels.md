# Channels：把 atlas 接到 IM 平台

这份文档回答两件事：**hermes-agent 的消息平台是怎么做的**，以及 **atlas 要接飞书 / QQ / 企业微信，应该落在哪一层**。

参考实现是 `hermes-agent/`（NousResearch/hermes-agent，MIT），它已经有现成的 `feishu`、`qqbot`、`weixin`、`wecom` 四个适配器——正好覆盖目标平台，所以下面讲的是"读它的代码能直接学到什么"，不是抽象设计。

先给结论：

1. **hermes 值得抄的是四件套**：适配器契约（5 个必需方法）、归一化事件（`MessageEvent`）、会话键（`build_session_key`）、每聊天串行队列 + 去重 + 白名单。这四样与语言无关，换成 Go 一样成立。
2. **三个目标平台的难度差一个数量级**：飞书最容易（官方 Go SDK 有长连接，不需要公网 IP），QQ 次之（官方 v2 API，但被动回复有时效），微信个人号没有面向开发者的官方机器人接口——官方路径是公众号的客服消息或企业微信，而 hermes 走的是腾讯 iLink 的非公开接口 + 扫码登录，不建议作为生产路径。
3. **atlas 的落点是第二个独立 module：`channel/`（`module pi-go/channel`），与 `desktop/` 同级**，二进制叫 `atlas-gateway`。独立成 module 是被 `tests/deps/run.sh` 逼出来的正确答案：它要求 `internal/` 除 `internal/tui` 外全部包的依赖闭包零第三方（"内核在空 module cache 里可构建"），而飞书适配器必须引官方 SDK——`desktop/` 为 cgo 独立成 module，网关为 SDK 独立成 module，是同一条规则的两个面。它需要的三个接缝 atlas 已经有了：`assembly.Options.Ask`（阻塞式审批回调）、`SessionPath`（会话落点）、`wire.AgentEvent`（增量事件）。**P0（骨架 + 飞书长连接 + 文本双向 + 绑定 + 白名单/配对 + 文本审批）已实现**，媒体与卡片仍是 P1。

两个平台的操作手册与验收细节已单独成文：[`channel-feishu.md`](channel-feishu.md)（P0 已落地）、[`channel-qq.md`](channel-qq.md)（仍是设计规格）。

## 1. hermes 的网关是怎么分层的

一个 `gateway` 进程同时跑 N 个平台适配器，每个适配器一条 asyncio 循环，事件先归一化再交给同一个 agent 运行时。

| 层 | 位置 | 职责 |
|---|---|---|
| 适配器 | `gateway/platforms/<platform>.py` | 连平台、收事件、发消息、媒体上下行 |
| 归一化 | `gateway/platforms/event.py` | `MessageEvent` / `MessageType`：文本、图片、语音、文件、命令 |
| 会话键 | `gateway/session.py` 的 `build_session_key` | 由"谁在哪儿说"推出一个确定性的会话标识 |
| 准入 | `gateway/platforms/access_policy_mixin.py` + `authz_mixin.py` | 白名单、配对、群策略、@ 门控、机器人互斥 |
| 投递 | `gateway/delivery.py` + `platforms/base.py` | 分片、重试、媒体、回执、失败上报 |
| 进程编排 | `gateway/run.py` | 起停、重连看护、控制面、cron 投递 |

**适配器契约**（`gateway/platforms/base.py`，4653 行；指南见 `gateway/platforms/ADDING_A_PLATFORM.md`）：

| 必需 | 作用 |
|---|---|
| `connect(is_reconnect=False) -> bool` | 建立连接并开始收；重连时要把平台的待投递队列保住 |
| `disconnect()` | 停监听、关连接、取消任务 |
| `send(chat_id, content, reply_to, metadata) -> SendResult` | 发一条文本（可能是 markdown） |
| `send_typing(chat_id)` | "正在输入"心跳 |
| `send_image(chat_id, url, caption)` | 发图 |
| `get_chat_info(chat_id) -> {name,type}` | 群名/类型，给上下文用 |

可选：`send_document` / `send_voice` / `send_video` / `send_animation` / `send_image_file`，以及三类交互式 UX——`send_clarify`（多选）、`_send_exec_approval_prompt`（危险命令审批）、`send_choice_picker`（`/model` 之类的选择器）。按钮回调 id 有统一约定（`appr:<id>:<choice>`、`cl:<id>:<idx>`、`sc:<choice>:<id>`），所以网关侧的解析器一套通吃所有平台。

**会话键**的布局是 `<ns>:<platform>:<chat_type>[:<scope>][:<chat_id>][:<thread_id>][:<user>]`（`gateway/session.py`）：

- DM 按 `chat_id` 隔离，`chat_id` 缺失时退到发送者 id，再退到"整平台一个会话"；
- 群聊默认所有人在同一个会话里，只有 `group_sessions_per_user` 打开且不在话题里时才按人拆；
- 话题（thread）默认共享，`thread_sessions_per_user` 才按人拆。

这个函数是"同一段对话落在同一个 agent 上"的唯一判据，适配器不许自己拼 key——hermes 甚至有一条 lint 规则（`scripts/check_profile_scope_patterns.py` 的 P32）专门抓自己拼键。

**准入**是可组合的几道闸，不是一个大 if：

- `allowed_users` / `allow_all` / `allow_bots`（机器人默认拒，防止两个 bot 互刷）；
- DM 策略：`pairing`（首次私聊发配对码，批准后才放行）、`open`、`allowlist`、`disabled`；
- 群策略：`open` / `allowlist` / `admin_only` / `disabled`，且默认 `require_mention=true`；
- 自消息回显过滤（`self_echo`）与消息 id 去重（`MessageDeduplicator`，重连时由运行器把旧 id 复制进新适配器）。

**投递**的三个细节值得记：分片按平台 `MAX_MESSAGE_LENGTH` 切；`reply_to` 只用在第一片；永久错误（`invalid` / `forbidden` / `not found` / `bad request`）不重试，其余按 1s / 2s 退避重试三次。

## 2. 四个现成适配器的实录

| 平台 | 接入方式 | 入站 | 出站 | 媒体 | 关键限制 |
|---|---|---|---|---|---|
| 飞书 | `lark-oapi` SDK，WebSocket 长连接或 HTTP webhook | `im.message.receive_v1`、表情、卡片回调、进群/退群、云文档评论、会议邀请 | `im/v1/messages`（可 update/patch 做"边写边改"） | 图片/视频/语音/文件，先上传换 key | 8000 字分片；群聊默认必须 @ |
| QQ | 官方 Bot API v2：`bots.qq.com` 取 token + WebSocket 网关 + REST | `C2C_MESSAGE_CREATE`、`GROUP_AT_MESSAGE_CREATE`、`DIRECT_MESSAGE_CREATE`、`INTERACTION_CREATE` | `/v2/users/{openid}/messages`、`/v2/groups/{group_openid}/messages` | 分片上传，`msg_type=7` | 被动回复 5 分钟 / 最多 5 次；群消息不支持流式 |
| 微信（个人号） | 腾讯 iLink Bot API：`ilinkai.weixin.qq.com`，扫码登录 | `ilink/bot/getupdates` 长轮询 + `get_updates_buf` 游标 | `ilink/bot/sendmessage` | AES-128-ECB 加密的 CDN | 出站必须回带该 peer 的 `context_token`；**非公开文档接口** |
| 企业微信 | 两种：智能机器人 `openws` WebSocket；自建应用 HTTP 回调 | 智能机器人事件；回调是加密 XML | 智能机器人走 ws；自建应用走 `message/send` | 有 | 回调必须"先 ack 再异步回" |

### 飞书（`plugins/platforms/feishu/`，4366 行）

- 两种传输共用一个处理器：`_handle_message_event_data` 同时服务 WebSocket 与 webhook，webhook 额外做 verification-token 校验与限流统计。
- 订阅的事件不止消息：`im.message.reaction.created/deleted`（被当作"合成文本事件"再入队，所以用户点个表情也能触发 agent）、`card.action.trigger`（卡片按钮）、`im.chat.member.bot.added/deleted`、`drive.notice.comment_add_v1`、`vc.bot.meeting_invited_v1`。
- 身份模型：`open_id`（应用内唯一，事件必带）、`user_id`（租户内，需权限）、`union_id`（开发者内稳定）。会话键优先 `union_id`，因为换应用后 `open_id` 会变。
- 入站内容抽取后做三件事：去掉 @ 机器人的边角、`/` 开头的转成 `COMMAND`、把 @ 提示拼回正文；随后 `build_source` 造 `SessionSource`，`handle_message` 交给网关。
- 群聊门控在 `_admit`：机器人策略、DM 策略、群策略、`require_mention`，四道依次过。`require_mention=false` 只对"合成事件"（表情、卡片点击）豁免。
- 卡片回调是同步返回的：`P2CardActionTriggerResponse` 里直接带回更新后的卡片，这样多个客户端看到的状态一致；异步的解析再排到 loop 上。
- 分片上限 8000 字（`MAX_MESSAGE_LENGTH = 8000`），`send()` 自己切。

### QQ（`plugins/platforms/qqbot/`，1677 行）

- 鉴权：`POST https://bots.qq.com/app/getAppAccessToken` 换 `access_token`，有效期 7200 秒，适配器在 `过期时间 - 60s` 时刷新。
- 网关：`wss://api.sgroup.qq.com/websocket`，标准 op 码——`10` Hello（给 `heartbeat_interval`，按 80% 发 `op 1` 心跳）、`2` Identify、`6` Resume、`0` Dispatch、`7` 服务端要求重连、`9` 会话失效（`d=true` 可 resume，`d=false` 重新 identify）、`11` 心跳 ACK。
- intents 用位掩码：`GROUP_AND_C2C_EVENT (1<<25) | PUBLIC_MESSAGES (1<<30) | DIRECT_MESSAGE (1<<12) | INTERACTION (1<<26)`——`1<<25` 同时覆盖群 @ 与单聊，这也是 hermes 代码注释里那四个名字的组合。
- 出站：`POST /v2/{users|groups}/{id}/messages`，`reply_to` 取入站 `msg_id`，并带自增的 `msg_seq`——同一个 `msg_id + msg_seq` 重复发送会被平台拒成"消息被去重"。
- 群消息必须 @ 机器人；要收全部群消息需要额外申请 `GROUP_MESSAGE_CREATE` 权限（默认只有 `GROUP_AT_MESSAGE_CREATE`）。
- 语音有福利：QQ 事件自带 `asr_refer_text`（平台侧转写），适配器先用它，失败才落到自己配置的 STT。

### 微信个人号（`plugins/platforms/weixin/`，1448 行）

- 传输是**长轮询**而不是 WebSocket：`POST ilink/bot/getupdates`，带上一轮的 `get_updates_buf` 游标，服务端 hold 住直到有消息或超时。
- 出站 `POST ilink/bot/sendmessage`，**必须回带该 peer 最近一次的 `context_token`**，否则发不出去；`context_token` 还在磁盘上做了按"账号 + peer"的缓存。
- 媒体走 AES-128-ECB 加密的 CDN（`novac2c.cdn.weixin.qq.com`），密钥来自接口返回的 base64，适配器里有 `_aes128_ecb_encrypt/decrypt` 与 PKCS7 补位。
- 登录是扫码：`hermes gateway setup` 里跑 `qr_login`，拿 `account_id` + `token` 存进配置。
- 这条路径没有公开的开发者文档，属于"能跑但不承诺"的接口。

## 3. 三个目标平台怎么选

| 平台 | 推荐路径 | 需要什么 | 难点 | 风险 |
|---|---|---|---|---|
| 飞书 | 企业自建应用 + **长连接** | App ID / App Secret；开通机器人能力与 `im:message` 相关权限 | 长连接要求 3 秒内处理完事件，否则平台重推；每应用最多 50 条连接；集群模式下同一条事件只有一个客户端收到 | 低 |
| QQ | QQ 开放平台机器人（官方 v2） | AppID / Client Secret；群聊场景要在沙箱里加测试群 | 被动回复 5 分钟 / 最多 5 次，超时要转主动消息，而主动消息有频控（认证 60 qpm，未认证 30 qpm；单群 20 qpm、每天 1000 条） | 中（限流与时效会塑造产品形态） |
| 企业微信 | 智能机器人（`openws`）或自建应用回调 | 机器人 ID / Secret，或自建应用的 CorpID + 回调 AES Key | 回调模式要自己实现 AES-CBC + PKCS7 + 签名校验，并且必须"先 ack 再回" | 低-中 |
| 微信个人号 | iLink（扫码登录） | 一个微信号 | 非公开接口、`context_token` 语义、加密 CDN | 高：账号限制/封禁，不适合生产 |

如果只做一件事：**先做飞书**。理由不是它最重要，而是它把"长连接 + 事件订阅 + 卡片审批"三件事一次性解决，且官方 Go SDK 已经封装了 WebSocket 与签名。核实过的细节（飞书官方文档）：

- Go SDK 是 `github.com/larksuite/oapi-sdk-go/v3`，长连接入口是 `larkws.NewClient(appID, appSecret, larkws.WithEventHandler(...))`，事件注册用 `dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(...)`——长连接模式不需要 verification token 或 encrypt key，SDK 内置加密与鉴权。
- 长连接**仅支持企业自建应用**，不支持商店应用；服务器只要能出公网，不需要公网 IP 或域名。
- 事件必须在 3 秒内处理完且不抛异常，否则触发超时重推；每个应用最多 50 个连接；集群模式不广播。

QQ 的两条硬限制同样来自官方 API 文档，不是 hermes 的取舍：

- `msg_id` 被动回复 **5 分钟内有效，同一条消息最多回 5 次**（超限错误码 `40034128`）；
- 主动消息有频控，单群每天最多 1000 条；`msg_seq` 用于"同一条入站消息的多次回复"，重复的 `msg_id + msg_seq` 会被当成重复消息拒掉（`40054005`）。

这两条直接决定了 QQ 上的交互设计：**不能"边想边说"**（群消息不支持流式），只能先回一句"收到，正在处理"占用第 1 次被动回复，再把结果压进剩下几次；超过 5 分钟的结果只能走主动消息，且要计入配额。

## 4. atlas 应该落在哪一层

### 包的位置

```
cmd/atlas            命令行前端（不动）
desktop/             窗口前端（Wails，独立 module，不动）
channel/             新增：IM 网关，独立 module（module pi-go/channel）
  ├─ channel.go      通用契约：Adapter / Inbound / Target / Capabilities / Sink
  ├─ gateway.go      进程编排：路由、每聊天串行 worker、审批应答、/stop
  ├─ runner.go       Runner 契约 + assemblyRunner（每绑定一个内核 slot）
  ├─ binding.go      会话映射：聊天 ↔ atlas 会话文件（bindings.json）
  ├─ approval.go     审批等待（5 分钟超时=拒绝，轮取消=拒绝）
  ├─ chunk.go        分片（8000 字，段落边界优先）
  ├─ commands.go     聊天命令：/new /stop /status /cwd /help
  ├─ dedupe.go       消息去重（LRU 环 + 落盘）
  ├─ allowlist.go    运行时白名单（approve 写这里，mtime 热加载）
  ├─ pairing.go      配对码（30 分钟有效）
  ├─ config.go       channels.json 的读写与模板
  ├─ feishu/         飞书适配器（长连接，已实现）
  └─ cmd/atlas-gateway/  二进制：run + approve
```

三条边界：

1. **channel 包不进口 `internal/app`、`internal/agent`、`internal/provider`。** 它和 `cmd/atlas`、`desktop` 一样，只通过 `assembly` 造内核、通过 `wire` 读事件。这条线一旦破了，网关就会开始替内核决定"什么算一轮"。
2. **内核不认识网关。** `assembly.Options` 一个字段都没加，网关只是又一个 `Ask` 的实现者；`tests/deps/run.sh` 在内核模块里根本看不见这个 module。
3. **本轮没有抽 `internal/kernelpool`。** `internal/webhost/sessions.go` 有一份"按会话路径持有内核"的 slot 实现，网关的 runner 另有一份按绑定键的——两份逻辑相同、键不同，等 webhost 与 channel 都稳定后把公共段抽成共享包；webhost 正在被另一条线修改，现在不动它。

### 契约

```go
// Adapter 是一个平台的传输。它的方法与 hermes 的必需方法一一对应，
// 只是把 Python 的可选参数换成了显式的能力声明。
type Adapter interface {
    Name() string
    Run(ctx context.Context) error            // 长连接 / 长轮询 / 回调服务
    Send(ctx context.Context, to Target, msg Outbound) (MessageID, error)
}

// Capabilities 让上层知道"能不能边写边改、一条能装多少字"。
// 飞书可以 update，QQ 群不行，微信只能纯文本——这三件事不该在上层用 if 判断。
type Capabilities struct {
    Edit        bool   // 支持更新已发消息
    MaxRunes    int    // 单条上限
    Markdown    bool   // 支持 markdown
    Streaming   bool   // 支持平台侧流式（群 QQ 明确不支持）
}

type Inbound struct {
    Adapter  string
    Chat     Target    // 平台、chat id、类型（dm/group/thread）、thread id
    Sender   User      // 平台用户 id（openid/open_id/userid）+ 显示名
    Text     string
    Files    []InboundFile // 已下载到本地临时目录
    MessageID string       // 平台消息 id：去重键，也是 QQ 被动回复的 msg_id
    At       time.Time
}
```

`Target` 就是会话键的原料：`(Adapter, ChatID, ThreadID)`。**不要在适配器里拼字符串键**——那是 hermes 用 lint 守住的地方，atlas 用类型守住。

### 会话映射

一个聊天对应一个 atlas 会话文件，绑定表落在 `$ATLAS_HOME/channels/bindings.json`（运行时状态目录 `$ATLAS_HOME/channels/` 下还有 `dedupe.json`、`pairing.json`、`allowlist.json`）：

```json
{ "feishu:oc_xxx": { "session": "/Users/crane/.atlas/sessions/2026/11/20/xxx.jsonl", "cwd": "/Users/crane/work/proj" },
  "qq:group:ABC": { "session": "...", "cwd": "..." } }
```

规则与 hermes 的 `build_session_key` 对齐，但用 atlas 的落点表达：

| 聊天类型 | 会话粒度 | 理由 |
|---|---|---|
| 私聊 | 每个用户一个会话 | 同一个人接着上次说 |
| 群聊（无话题） | 每个群一个会话 | 群是共享上下文，按人拆会让"刚才说的"消失 |
| 群聊（有话题/thread） | 每个话题一个会话 | 飞书话题就是天然的并行分支 |
| QQ 群 | 每个群一个会话 | 与官方 @ 语义一致 |

第一次见到某个聊天时：`assembly.New(Options{SessionDir: ..., Workdir: bindingCWD, Ask: approver})` 造内核，`SessionPath` 留空表示新会话；之后每轮复用同一个 slot。落盘时机沿用 atlas 的会话语义（第一句话之后才有文件）。

### 一轮怎么跑

```
Inbound → 准入（白名单/群策略/@）
        → 去重（MessageID，LRU + 落盘）
        → 取/建 slot（每聊天一个内核）
        → 立即回执（可选：typing / "收到"）
        → slot.App.Ask(ctx, wire.UserMessage(text), emit)
             emit(wire.AgentEvent) ──► 聚合器 ──► 适配器.Send / Edit
        → 轮末：状态（成功/失败/耗时）附在最后一条消息上
```

聚合器是这套设计里唯一有平台知识的地方：

- `text_delta` 累积到缓冲，按 `Capabilities.Edit` 决定"发一条然后不断 edit"还是"攒到轮末发一条"；
- 工具调用结束时是一个天然的分段点（"我在查 X"），可以在这里 flush 一次，让读者知道进度；
- 超过 `MaxRunes` 就切片，`reply_to` 只用第一片（hermes 的做法，避免平台把每一片都当引用回复）；
- 失败/超时也要发一条——IM 里"没有反应"比"报错"更难判断。

### 审批怎么过

这是 atlas 比 hermes 省事的地方：`assembly.Options.Ask` 本来就是一个阻塞回调，终端用按键回答，窗口用阻塞 POST 回答，IM 用消息回答——三者是同一个形状。

```go
ask := func(q assembly.Ask) assembly.Answer {
    id := approvals.open(q)              // 记进 pending 表
    defer approvals.close(id)
    adapter.Send(ctx, target, question(q))  // "要运行 bash: rm -rf build，允许吗？"
    select {
    case a := <-approvals.wait(id):      // 入站消息解析成 y / a / n
        return a
    case <-time.After(5 * time.Minute):
        return assembly.Answer{}         // 超时=拒绝，与窗口关闭同语义
    case <-ctx.Done():
        return assembly.Answer{}
    }
}
```

两个要点：

- **等待不能阻塞入站循环**。审批的答案来自下一条入站消息，所以 inbound loop 必须继续跑，用 `request_id` 把答案路由回等待者。飞书可以用卡片按钮（`appr:<id>:<choice>` 的等价物），QQ 用内嵌键盘，微信只能文本 `y/n/a`。
- **只有被授权的人能回答**。审批消息可能发在群里，答案必须来自发起者或管理员；否则群里任何人都能替 owner 批准 `rm -rf`。

### 配置

网关有自己的文件 `$ATLAS_HOME/channels.json`（首次运行会写出模板），**不在 `config.json` 里加段**：配对批准只需要追加运行时白名单文件，永远不必重写用户手编的配置；这也让网关与内核的配置互不拖累。顶层键是平台名，每个平台的字段由自己的包解释：

```json
{
  "feishu": { "appId": "cli_xxx", "appSecret": "env:ATLAS_FEISHU_APP_SECRET",
              "dmPolicy": "pairing", "groupPolicy": "allowlist", "requireMention": true,
              "admins": ["ou_owner"], "cwd": "/Users/crane/work" },
  "qq":     { "appId": "...", "clientSecret": "env:ATLAS_QQ_CLIENT_SECRET",
              "groupPolicy": "allowlist", "groups": [], "dmPolicy": "pairing" },
  "wecom":  { "botId": "...", "secret": "...", "mode": "ws" }
}
```

三个运行时文件与手编配置分开放：`allowlist.json`（approve 追加、适配器按 mtime 热加载）、`bindings.json`（聊天 → 会话）、`dedupe.json` 与 `pairing.json`。密钥支持 `"env:VAR"` 引用（环境变量 `ATLAS_FEISHU_APP_ID` / `ATLAS_FEISHU_APP_SECRET` 直接覆盖配置）；日志里对 openid / 手机号脱敏——hermes 在 `agent/redact.py` 里专门做了一层，atlas 至少要保证"不把消息正文写进 INFO 日志"。

## 5. 分阶段落地

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0（已实现） | `channel/` module + `atlas-gateway` 二进制 + 飞书长连接 + 文本双向 + 会话绑定 + 白名单/配对 + 文本审批 | 逻辑部分由 `go/channel` 的单元测试覆盖（整链路、审批路由、/stop、去重、分片、准入）；真机部分（长连接、卡片之外的真实收发）待有飞书应用凭据后按 [`channel-feishu.md`](channel-feishu.md) 验收 |
| P1 | 审批走 IM（飞书卡片按钮 + 文本兜底）、图片入站、轮末状态 | 危险命令会在飞书里等批准；拒绝后工具不执行；5 分钟超时=拒绝 |
| P2 | QQ 官方 v2：WS + 被动回复 + `msg_seq` + 5 分钟预算（先回执再答） | 群里 @ 能答；超过 5 次的回答自动降级主动消息并受频控；私聊可用 |
| P3 | 企业微信智能机器人（WS）；回调模式后补 | 群里可用；回调模式能通过签名与 AES 校验 |
| 不建议 | 微信个人号 iLink | 除非明确接受账号风险；实现参考 hermes 的 `weixin/adapter.py` |

每个阶段的共同验收项：重复投递不重复回答（同一 `MessageID` 只处理一次）、断线重连后不丢不重、长回答按平台上限分片、错误在聊天里可见而不是只在日志里。

## 6. 不要照抄的地方

- **别抄 hermes 的插件目录机制**。它是 Python 动态导入 + `plugin.yaml`，atlas 是静态编译的 Go；"加一个平台 = 加一个包 + 一行注册"更合适，插件化可以等有第三方需求再说。
- **别抄"每平台一套环境变量"**。hermes 每个平台 8~15 个变量，是历史累积；atlas 的一份 JSON 更清楚。
- **别抄它的 streams/卡片体系**。飞书的卡片、QQ 的 keyboard、微信的纯文本差异太大，抽象成"三种消息形态 + 能力声明"就够；一上来做统一富文本模型会得到谁都不像的东西。
- **别在网关里重算"一次工具调用"**。`wire.AgentEvent` 已经说了；网关只做"怎么呈现"，不做"什么算一段"。

## 7. 待核实

- QQ 的 `msg_seq` 是否对 C2C 与群都一样（hermes 是同一套自增，官方文档只明确写了群）；被动回复的 5 分钟从"收到消息"还是"首次回复"起算，需要联调确认。
- 飞书长连接在"3 秒内处理完"这条上，官方建议是"先回 200 再异步处理"，但 SDK 的 `OnP2MessageReceiveV1` 是同步回调——正确做法是把事件投进本地队列后立刻返回，处理在另一个 goroutine，这一点要按 SDK 版本验证。
- 企业微信智能机器人的 `openws` 协议没有公开文档，hermes 的实现是唯一参考；如果这层不可用，退回到自建应用回调（文档齐全）。
- `ilink` 的 `context_token` 生命周期（多久失效、是否每个 peer 独立）只能从 hermes 的缓存策略反推，没有官方说明。
