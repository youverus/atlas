# MCP：抄什么、不抄什么

这份文档有两半。前八节是动手前的依据：MCP 协议现在长什么样、pi 的客户端怎么写的、atlas 的哪几条约束决定了取舍。第九节是第一版的范围与验收，第十节是未解问题，第十一节是落地之后记"计划被哪几处实测推翻"，第十二节是长任务那一轮（`GET` 流与 `notifications/progress`）的取舍与落地记录。

**MCP 已经落地（0.7.0）**：`internal/mcp`（协议与传输）、`internal/config/mcp.go`（配置）、`cmd/atlas/mcp.go`（接线与 `/mcp`）。所以这份文档和 [`sandbox.md`](sandbox.md) 一样有两层：第 1 到 10 节写在动手之前——传输范围、工具暴露方式、要不要复用沙箱这三件事是架构级的，先定下来才不会返工——并且保留原文，不按落地结果回改，否则"当时的判断"就没法检验；第 11 节写第一版落地之后，第 12 节写长任务那一轮。

标注约定：**【规范】** 从 MCP 官方规范读到的；**【pi】** 从 pi 的 `packages/mcp` 与 `packages/coding-agent/src/extensions/mcp` 读到的；**【atlas】** 我们自己的决定。

## 1. 为什么客户端必须自己写

atlas 的依赖准入是一条机械检查：`internal/` 下除 `tui` 之外不许有第三方（`tests/deps/run.sh` 查的是依赖闭包，所以间接摸到也算）。MCP 有官方 Go SDK，它是第三方，进不了内核包。

两条路：

- 把客户端写进 `cmd/atlas`。那里允许第三方。代价是客户端不可单独测试，组合根再涨一千多行，而 `tests/cli/run.sh` 只查"是不是唯一导入具体适配器的包"，管不了协议正确性。
- 自己写，只依赖标准库。

选后者。要用到的协议面很小：JSON-RPC 2.0 的编解码、两个传输、约十个方法。作为规模参照，pi 的 `client.ts` 是 621 行、两个传输 737 行，其中一多半是 OAuth（另有约 1100 行）与资源渲染：第一版两块都不做，OAuth 后来单独做了一轮（[`mcp-oauth.md`](mcp-oauth.md)），资源渲染仍然不做。

这不是"反 SDK"，是"内核只认标准库"。而且 SDK 的 `Client` 带着它自己的工具类型，接进 `internal/tool` 还要再包一层——那层包装本来就是我们要写的东西。

## 2. 协议的骨架：现在有两代

**【规范】** 当前修订（2026-07-28）把核心改成了无状态：取消 `initialize` 握手与协议级会话，每个请求用 `_meta["io.modelcontextprotocol/protocolVersion"]` 自描述（Streamable HTTP 同时带 `MCP-Protocol-Version` 头）；新增强制 RPC `server/discover`；`Mcp-Method` / `Mcp-Name` / `Mcp-Param-*` 头给网关做路由；服务端到客户端的长连双向流由 MRTR 取代；list 结果可缓存；Tasks 转为官方扩展。

上一稳定版是 **2025-11-25**（OIDC Discovery、图标元数据、URL 模式 elicitation、OAuth 的 CIMD、实验性 Tasks）。再往前 2025-06-18 移掉了 JSON-RPC batching 并加了结构化工具输出。

**【规范】** 两代可以共用一个端点，客户端按自己说的版本各走各的。所以"支持哪一代"是客户端的选择，不是端点地址的问题。

**【pi】** 接受 `2025-11-25` / `2025-06-18` / `2025-03-26` / `2024-11-05`，握手是 `initialize` → 校验响应里的 `protocolVersion` 在支持列表内 → `notifications/initialized`。客户端被要求 `roots/list` 时由配置的 roots 回答，不认识的方法回 `-32601`，超时或取消时发 `notifications/cancelled`。

**【atlas】** 第一版钉 2025-11-25 这一代。三条理由：

1. 这台机器上唯一能对照的实现（pi）至今只接受握手一代的四个版本，没有 2026-07-28；能拿来测的服务器也都是它那一代。
2. 无状态一代要求客户端按请求自描述、实现 `server/discover`，等于第二套状态机；而它省掉的"会话"在单机客户端里本来就不是成本。
3. 我们的测试全离线，能验证的只有自己写的假服务器。假服务器能回答的是"我们按规范说话吗"，而规范里被实现得最广的那部分是握手。

退路留一条：服务器回的 `protocolVersion` 只要在接受的列表里就用它的，不在列表里就报一句带版本号的话并停掉这个服务器。是"钉一代"，不是"只认一个字符串"。

## 3. stdio

**【pi】** 分帧：按 LF 切、容忍行尾 `\r`、跳过空行、单条消息有字节上限；进程退出时缓冲区里还有残片 → 报 `incomplete JSON-RPC message`，不静默丢。

**【pi】** 关闭顺序：关 stdin → 500ms 宽限 → SIGTERM → SIGKILL，**杀整个进程组**（`npx` / `uvx` 起的子进程一起死）；stderr 保留尾部 64KB 供诊断。

两条都要抄，理由各一条：

- 进程组是 `internal/tool/shell` 已经踩过的坑（`process_unix.go` 的 `Setpgid`）。stdio 服务器是同一类东西：配置里写的是 `npx -y @scope/server`，真正干活的服务器是它的孙子进程。只杀直接子进程就是留孤儿。
- stderr 是唯一的诊断通道。JSON-RPC 走 stdout，服务器把日志写 stderr；不留尾部，用户看到的就是"连不上，没有原因"。atlas 的做法是把尾部放进那句 warning 里（截成一行），不写日志文件。

**【atlas】** stdout 只许是协议。任何一行不是合法 JSON-RPC 就报错并停掉这个服务器——不跳过、不重试。跳过等于把"服务器顺手打印了调试信息"变成"偶发丢响应"，而丢响应会让某次工具调用永远挂着。

## 4. Streamable HTTP

**【规范】【pi】** 形状：

- `POST` 一条消息，`Accept: application/json, text/event-stream`；响应可能是单条 JSON，也可能是一段 SSE。
- 通知与响应回 `202` 或 `204` 无体。
- 带 `Mcp-Session-Id`（服务器在 `initialize` 的响应里给）与 `MCP-Protocol-Version` 头。
- `401` → 需要认证。
- 带 session 的 `404` → 会话过期，重新 `initialize` 一次。
- `DELETE` 结束会话。
- `GET` 开一条服务器到客户端的流，支持 `Last-Event-ID` 续传与 `retry` 字段。

**【atlas】** 第一版只做请求-响应那半：`POST` 与 `DELETE`。`GET` 那条流不做——它承载的是服务端主动发起的请求（sampling、elicitation、日志、进度），而第一版一个都不支持，开着它只会收到一堆要回 `-32601` 的东西。代价写清：有些服务器把长任务的进度通知放在这条流上，第一版看不到进度；工具调用本身仍会返回，只是没有中间更新。

**【atlas】后来的修正（长任务那一轮）：这条流补上了。** 理由变了：进度通知与 sampling / elicitation 走同一条流，而"看不见进度"不是一个可以留着的代价——它把 60 秒的 `timeout` 变成一条硬上限，一个跑 90 秒的构建会被当成卡死杀掉。于是 `GET` 在握手之后开一次，只认 `notifications/progress`，其余的入站请求照第 9 节的"不做"清单回 `-32601`。细节与代价见第 12 节。

### 一条要顺手改的依赖边

SSE 分帧想复用 `internal/provider/sse`：它的 `Event` 已经带 `ID` / `Name` / `Data` / `Retry`，`Reader.LastEventID()` 也在，正好是这条传输要的全部。

但那个包挂在 `provider` 下面，而 MCP 不是 provider。让 `internal/mcp` import 它，依赖图上就多出一条 `mcp → provider/sse` 的跨层边，闸门只能给它开例外——而 INDEX 的维护规则写的是"如果一条依赖箭头需要反向，说明分层划分有问题，不要加例外"。

**【atlas】** 所以落地时把包移到 `internal/sse`。它本来就只依赖标准库、不认识任何厂商的 JSON 结构，和 `wire`、`theme`、`sandbox` 是同一类叶子，`internal/` 根下才是它该在的位置。代价是三家适配器加四个闸门脚本改一个 import 路径，机械但不小；收益是 MCP 落地不带来例外，也不把"provider"这个词塞进一个不认识的包里。

反过来的选项（在 `internal/mcp` 里再写一份 SSE 读取）不做：WHATWG 的分帧规则已经有一份带 fixture 的实现，第二份只会漂。

## 5. 工具怎么进来

**【pi】** 名字是 `mcp__<server>__<tool>`，非 `[A-Za-z0-9_]` 一律换 `_`；超过长度上限或与别的 MCP 工具撞名时截断再加 `sha256(server\0tool)` 的前 8 位。Codex 用同一写法。

**【atlas】** 照抄这个命名。不是审美问题：这套配置格式的卖点就是跨客户端可迁移（Claude Desktop / Claude Code / Cursor / OpenCode / Codex 的条目能直接搬过来），名字不一致等于"同一台服务器在两个客户端里是两个工具"。

**【pi】** 暴露模型四档：`codemode`（默认，脚本可达但不声明）、`deferred`（`tool_search` 加载后才声明）、`direct`（直接声明）、`hidden`；`toolExposure` 可以按通配覆盖单个工具。

**【atlas】** 第一版只有两档：**声明**与**不声明**（按服务器的 `enabled`）。理由是另外两档在 atlas 里没有落点——`deferred` 需要一个能加载工具列表的工具，`codemode` 需要一个"以工具为函数"的脚本运行时（我们有 `bash`，但那是模型自己拼命令行，不是同一个东西）。做不了就不写配置项：写下的 `exposure: "deferred"` 只能被忽略或报错，两种都比"没有这个键"更差。

**【atlas】** 0.12.11 补了粒度更细的一档：per-server 白名单 `"tools": ["read_file", ...]`。它是 `toolExposure` 的**子集**，不是替代——没有通配、没有逐工具的档位，只有"这台服务器只交这几个工具出来"。理由是第 10 节第 2 条暴露的问题（几十条声明进系统提示词）只需要"少给几个"，而不需要一套暴露模型；白名单按服务器自己的拼写匹配，因为那是服务器答 `tools/call` 时认的名字。两个数字都留着：`Server.Declared` 是服务器声明的总数，`len(Server.Tools)` 是放行的数量，`/mcp` 因此能写"2 of 5"而不是一个没有参照的 "2"。

`tools/list_changed` 是同一轮补上的，它比白名单更深一层：**工具表不再是启动时的一张快照**。原来挡路的是 `app.Config.Tools` 是构造时定死的切片，这条从 0.12.6 起已经不存在（现在是 `ToolSet func(cwd)`），剩下要动的是 `agent`：`agent.New` 里 `tool.NewRegistry(cfg.Tools...)` 只装一次。现在 `App.Ask` 在每轮开始重绑 registry（`app.refreshTools`），和 `refreshPrompt` 并排，理由也一样——会话跑着的时候外面的东西会变。侧会话不跟：它的提示词与工具都停在打开那一刻，这是那边既有的快照规则。

接收点在**连接层**而不是传输层。通知到达的那条 goroutine 就是读连接的那条，所以重列必须 `go` 出去：在同一个 goroutine 里发请求会等一个只有它自己能投递的答案，直接死锁。重列需要游标循环（第 11 节第四点）、白名单和跨服务器的名字分配，这些都在 `Set` 手里，传输层看到的是消息不是方法。中间那段窗口也兜住了：第一份 `tools/list` 在 `Open` 里跑完，钩子要等 `Set` 建好才装得上——`client.watch` 返回"错过了一次"，`Set.watch` 据此补一次重列，否则慢一点的服务器的通知会掉在地上，现象是"工具表对，但少了刚加的那个"。

## 6. 结果怎么回去

**【pi】** 的转换：`content` 块（text / image / audio / resource_link / resource）→ 模型可见内容；`resource_link` 渲染成一行文本并提示用 `read_mcp_resource`；二进制资源存临时文件并给路径；`isError` 保留 `structuredContent` 并标记错误；模型可见文本超过 20KB 走中段截断加全文落临时文件。

**【atlas】** `tool.Result` 已经有 `StructuredContent` 与 `IsError`（`internal/tool/tool.go:28`），所以 `structuredContent` 与 `isError` 是直接落位，不新增字段。要处理的是三件：

1. **块类型映射。** `text` → 文本块；`image` → 图片块（`wire` 已有）。`audio` 与 `resource` 第一版降级成一行说明加临时文件路径——`wire.Content` 只有 text / thinking / toolCall / image 四种（`internal/wire/content.go:10`），为了音频编一个第五种类型比"少一种"贵得多。
2. **资源引用。** 第一版不提供 `read_mcp_resource`，所以 `resource_link` 与 `resource` 只能渲染成一行文本（URI 加名字），并说明 atlas 没有读资源的工具。不假装能读。
3. **体积。** 照抄"中段截断加全文落盘"，但要先补一块砖：`internal/tool/truncate.go` 今天只有 `TruncateHead` 与 `TruncateTail`，没有中段截断；补一个 `TruncateMiddle`（保留头尾、挖掉中间）比"第一版先砍尾巴"更对，因为工具结果的两头都有信息。落盘不复用 `shell` 的 `accumulator`——那是流式的、增量写、带 tick 的，MCP 只需要"结果到手之后写一次"。

**【pi】** 工具调用不重试（服务器可能已经执行），`tools/list` 与资源读取可以重试。

**【atlas】** 这条照抄。重试一个非幂等的副作用是 MCP 客户端最容易犯的错。而且"没得到答案"与"服务器说失败"必须分开：前者是传输错误（`Execute` 返回 error），后者是一个正常的工具结果（`Result.IsError`），模型对这两种的反应不该一样。

## 7. 配置的形状

**【pi】** `~/.pi/agent/mcp.json`（全局）加 `<project>/.pi/mcp.json`（只在项目被信任时读）。项目条目没有 `command` / `url` / `type` 时只覆盖 `enabled` / `exposure` / `toolExposure`，其余（含 `env` / `headers` / `auth`）继承。服务器名匹配 `[A-Za-z0-9_-]+`；只差 `-` / `_` 的两个名字算同一个，第二个被拒。字段有 `type`（可选，`stdio` / `http` / `streamable-http`，`sse` 拒绝）、`timeout`（秒，缺省 60，progress 会重置）、`enabled`、`exposure`、`toolExposure`、`description`（进系统提示词的服务器列表，也用于 tool search 排序）。`env` / `headers` 的值支持 `${NAME}` 与整值 `!command`。

**【atlas】** 第一版：

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/Users/me/work"],
      "env": {"TOKEN": "${MCP_TOKEN}"}
    },
    "docs": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"}
    }
  }
}
```

- 沿用 `mcpServers` 这个键名与 `command` / `args` / `env` / `cwd` / `url` / `headers` 这些字段名，理由同第 5 节：别的客户端的条目能直接搬。
- 两级文件：全局 `$ATLAS_HOME/mcp.json`，项目 `<cwd>/.atlas/mcp.json`（项目级目录沿用 `theme` 的约定，`internal/theme/library.go:292`）。项目级只覆盖 `enabled`，`command` / `url` / `headers` / `auth` / `scopes` / `tools` 出现在项目级就是错误——一个能改 argv 或加凭据的项目文件是仓库里的代码执行，而 atlas 没有 pi 的"项目被信任"那套。项目条目引用的名字必须在全局里存在，否则报错：静默忽略会让"我明明写了"变成一次猜谜。
- 桌面 MCP 开关按服务名称保存到 `$ATLAS_HOME/capabilities.json`，覆盖全局、插件和项目的 `enabled` 值。停用会关闭当前连接，并从后续对话轮次移除该服务的工具定义。重新启用只连接这一项。
- `type` 可省：有 `url` 就是 http，有 `command` 就是 stdio，两个都有或都没有就报错。`"type": "sse"` 明确拒绝并指向 streamable HTTP 的 `/mcp` 端点。
- `timeout` 秒，缺省 60，语义照抄 pi。第一版按调用计时而不是按会话，理由见第 10 节第 4 条。
- `description` 第一版不认：它进的是系统提示词的服务器列表，而 atlas 的 `system.md` 是静态的，加一段要动 `Prompts`（`internal/assembly/prompt.go`）。没有消费者的键不写进解析器。
- `${NAME}` 做，整值 `!command` 不做——后者要在配置里执行 shell，而 `${NAME}` 已经够表达"秘密别写在文件里"。
- 未知字段报错，照 `internal/config` 现有的严格解码（`config.json` 与 `tui.json` 都拒绝未知字段，块里的错报全路径）。
- `tools` 是 0.12.11 加的，字符串数组，按服务器自己的拼写。空串与重名两个都报错（前者是打错，后者是手滑）；白名单里写了服务器不声明的名字不报错但在启动时打一行 warning，因为"我明明写了"和"这个服务器没有"必须能分开。它属于项目级不许写的键：宽严都能改工具表，而宽严都是读者在全局里定的。

**【atlas】** 放 `internal/config` 还是新包：放 `internal/config`。它是配置文件的一部分，而 `config` 是标准库叶子；解析与校验在那里，连接与协议在 `internal/mcp`。`config` 不认识 `internal/mcp`，只交出一张 `map[string]Server`。

## 8. 安全边界

三个问题，一个都不在沙箱范围内，但都得写下来。

**一、`mcpServers` 是一个能执行代码的配置文件。** 这不是比喻，`command` 就是 argv。atlas 的沙箱已经把 `.mcp.json` 放进工作区级的拒绝清单（`internal/sandbox/profile.go:90`，注释写的是"它决定编辑器启动哪些服务器"）——也就是说我们在沙箱那一层早就把它当 argv 对待了。MCP 落地之后这句话要从"别的编辑器"改成"atlas 自己"。

**二、stdio 服务器不套沙箱。** 沙箱的边界是"谁写的命令行"：`bash` 被约束，因为命令行是模型写的；`mcpServers` 不被约束，因为它是用户写的，和 `atlas` 自己的二进制同级。而且套上就基本不能用：可写根是 cwd 与 `$TMPDIR`，而 `npx` 要写 `~/.npm`、要联网装包。所以第一版不套，并且要在 README 里写明"装一个 MCP 服务器等于给它你给的权限"。真正约束 MCP 的不是沙箱，是第 5 节的暴露范围——一个服务器能干什么由它自己的代码决定，客户端只能决定模型看不看得见它。

**三、凭据。** `headers` 里的 `Authorization` 是明文，和 `apiKey` 一样。第一版的答案是 `${ENV}` 展开加"项目级不许写 `headers`"（第 7 节），也就是秘密只可能出现在全局文件里，最好连那里也只有变量名。OAuth 不走这条路：它是另一条通道，由 `"auth": "oauth"` 显式打开，token 落在 `$ATLAS_HOME/oauth.json`（第 9 节、[`mcp-oauth.md`](mcp-oauth.md)）。

## 9. 【atlas】第一版的范围

做：

1. `internal/mcp`：JSON-RPC 2.0 编解码、stdio 传输、Streamable HTTP 传输（`POST` 与 `DELETE`）、`initialize` 握手、`tools/list`、`tools/call`、`ping`。
2. 每个远端工具包一层 `tool.Tool`：名字 `mcp__<server>__<tool>`，描述与 `inputSchema` 原样转，`Execute` 走 `tools/call`。
3. `internal/config` 新增 `mcpServers`（全局加项目两级，项目级只覆盖 `enabled`）。
4. `cmd/atlas` 新增 `mcpTools(cfg, cwd)`：启动时连所有 enabled 服务器，连不上只打一句 warning 且不注册，接进 `tools(cwd, webSet, confine)`（`cmd/atlas/main.go:607`）。形状照 `webTools`（`cmd/atlas/build.go:144`）：那句 warning 说清"这个会话里没有它的工具"，而不是让模型在一次空调用里发现。
5. 把 `internal/provider/sse` 移到 `internal/sse`（第 4 节）。
6. `tests/mcp/run.sh`：依赖闭包守卫（标准库加 `tool` / `wire` / `sse`，且不可达 `provider` / `agent` / `config` / `app`），加 `GOOS=windows` 的交叉 vet，加格式 / vet / test / race。测试用自己写的假服务器（测试二进制 `-test.run` 重入成子进程），不碰真实端点。

不做：

1. resources / prompts / sampling / elicitation / logging / completion。收到这些入站请求回 `-32601`。
2. ~~OAuth~~ **已落地**（见 [`mcp-oauth.md`](mcp-oauth.md)）：显式的 `"auth": "oauth"` 加 `/mcp login <server>`，走 protected-resource 发现 → 动态注册 → PKCE → 回环回调 → 刷新，token 落在 `$ATLAS_HOME/oauth.json`。这一条留在这里是因为它原来写的三条边界仍然成立：`401` 不会替你启动登录（不自动试探）、不保存密码、没有后台定时刷新。仍然不做的六件事在那边第 5 节（`client_secret_post`、step-up、CIMD、设备码流、自动开浏览器、多进程协调）。
3. ~~`tools/list_changed`（第 5 节）~~ **已落地**（0.12.11，见第 5 节与第 11 节第十二点）：通知一律 `go` 出去重列，跨启动窗口的那一次也不丢。同一轮补的 per-server 工具白名单 `tools` 见第 5、7 节。
4. ~~`GET` 那条服务器到客户端的流（第 4 节）~~ **已落地**（长任务那一轮，见第 12 节）：握手之后开一次，收 `notifications/progress`，`timeout` 因此从"总时长"变成"空闲上限"。
5. 2026-07-28 的无状态方言（第 2 节）。
6. `exposure` / `toolExposure` / `description` / `!command`（第 5、7 节）。白名单 `tools` 是 `toolExposure` 的子集，已落地；通配与逐工具档位仍不做。
7. 沙箱（第 8 节）。
8. Windows 的进程组：stdio 在 Windows 上只杀直接子进程，并在文档里写明；闸门只做交叉 vet，照 `tests/shell/run.sh` 的先例。

验收（每条都是一条会变红的测试，`tests/mcp/run.sh` 跑它们）：

1. 握手：假服务器回 `2025-06-18`，客户端接受并发出 `notifications/initialized`；回 `1999-01-01`，客户端报错且话里带版本号。→ `TestHandshakeAcceptsKnownVersions` / `TestHandshakeRefusesUnknownVersion`
2. 分帧：一行带 `\r`、一个空行、一条合法消息，只解析出一条；超长行报错并带上限。→ `TestStdioFraming`
3. 残片：服务器发半条消息后退出，报 `incomplete JSON-RPC message` 而不是 EOF。→ `TestStdioIncompleteMessage`
4. 命名：服务器 `my.server` 的工具 `read file` → `mcp__my_server__read_file`。→ `TestToolNamesAreNormalized`
5. 结果：三种 `content` 块加 `isError: true` 加 `structuredContent`，落成 `Result.IsError` 为真、`StructuredContent` 原样。→ `TestCallResultMapping`
6. 不重试：假服务器对第一次 `tools/call` 回 500，客户端只发一次请求。→ `TestCallsAreNotRetried`
7. 关停：服务器自己再起一个子进程，客户端关停后两者都死。→ `TestStdioKillsTheProcessGroup`
8. HTTP：`Accept` 两个都带；`initialize` 响应里的 `Mcp-Session-Id` 在后续请求里回传；带 session 的 `404` 触发一次重新 `initialize`。→ `TestStreamableHTTPSession`
9. 配置：项目级出现 `command` 报错；`type: "sse"` 报错并指向 `/mcp`；未知字段报错；项目条目引用不存在的名字报错。→ `TestConfigRefuses`
10. 连不上：`command` 不存在的服务器，会话照常开始，工具列表里没有它，stderr 有一行 warning 带 stderr 尾部。→ `TestUnreachableServerWarnsOnce`
11. OAuth：设计文档第 7 节那九条各一条测试（PRM 的三个候选路径、issuer 不符时不发注册请求、只注册一次、PKCE 只做 S256、回调 state 不符不投递、并发 401 只刷新一次、`oauth.json` 的 0600 与半截文件、声明 oauth 而没 token 时零请求、`WWW-Authenticate` 的引号与非引号），都在 `internal/mcp/oauth_test.go` 里，仍由同一个闸门跑。→ `TestProtectedResourceMetadata` / `TestIssuerMismatchStopsBeforeRegistering` / `TestRegistersOnce` / `TestPKCE` / `TestCallbackState` / `TestRefreshOnce` / `TestOAuthStore` / `TestOAuthServerWithoutTokenDoesNotConnect` / `TestParseChallenge`
12. 长任务（第 12 节）：五条 tick 的调用在 120ms 的空闲预算下跑完（每条进度都把时钟推后）、服务器沉默时 80ms 后放弃且那句话里带 `silent for 80ms` 与 `"timeout"`、进度从 `POST` 的答案流与 `GET` 各来一次都要接住、`GET` 回 405 的服务器照常调工具、`GET` 上不认识的通知被丢掉而不是停读、文案优先用服务器给的那句话、没有对应调用的进度被丢弃。→ `TestProgressKeepsCallAlive` / `TestSilenceExpiresTheCall` / `TestProgressFromTheAnswerStream` / `TestProgressFromTheGetStream` / `TestHTTPWithoutStreamStillCallsTools` / `TestGetStreamDropsWhatItDoesNotUnderstand` / `TestProgressTextPrefersTheServerMessage` / `TestProgressWithoutACallIsDropped`

13. 会变的工具表（第 5 节）：服务器在会话中途加一个工具，下一次列出来就在；握手期间发来的通知不漏；白名单在重列之后仍然生效，并留下"服务器声明了几个"；白名单写了服务器没有的名字要报出来；重列失败时旧工具表留着并记下原因。→ `TestAToolDeclaredMidSessionJoinsTheSession` / `TestAChangeThatArrivesBeforeTheHookIsNotLost` / `TestARefreshKeepsTheWhitelist` / `TestAWhitelistNarrowsTheToolList` / `TestAWhitelistThatNamesNothingIsReported` / `TestAFailedRefreshKeepsTheToolsItHad`；内核那一半是 `TestAToolThatAppearsBetweenTurnsIsCallableOnTheNextOne` / `TestAToolThatDisappearsStopsBeingCallable`（`internal/app/tools_test.go`）。

## 10. 未解问题

1. **两代协议并存。** 第 2 节钉了 2025-11-25，而无状态一代意味着客户端里"会话"这个概念整个消失。等有真实服务器说自己是 2026-07-28 再评估；在那之前，我们的传输层状态机是为握手一代写的，这不是能两头兼顾的东西。
2. **工具数量与提示词成本。** ~~每个 MCP 工具都进系统提示词，装三个服务器就可能多出几十条声明，而 atlas 没有 `tool_search` 去挡。第一版只有"整个服务器开关"，粒度太粗——真正需要的是 per-server 的工具白名单（pi 的 `toolExposure` 就是干这个的）。什么时候做取决于装了几个服务器之后 token 账单长什么样。~~ **已解决**（0.12.11）：`"tools"` 白名单按服务器自己的拼写收窄（第 5、7 节）。没解决的是 token 成本本身——白名单要靠读者自己写，atlas 不会替他算哪几个工具值得留；`tool_search` 这一档仍然没有，`deferred` 也就仍然没有落点。
3. **名字撞车的哈希后缀。** `mcp__<server>__<tool>` 已经含服务器名，真正的撞车只可能来自字符替换（`a.b` 与 `a_b` 两个工具）。第一版按 pi 的做法加 8 位哈希，但如果从来没撞过，那就是模型看到的一个没有意义的尾巴。要不要在第一版就做，等有真实案例再定。
4. **进度与长任务。** ~~不做 `GET` 流意味着看不到 `notifications/progress`，而 pi 的 `timeout` 正是被 progress 重置的。第一版的时间上限是硬的：一个跑 90 秒的工具会被 60 秒的 `timeout` 杀掉。第一版的答案是按调用给超时而不是按会话，真正的解法是 progress。~~ **已解决**（第 12 节）：`GET` 流与 `notifications/progress` 都通了，`timeout` 改成空闲上限；界面那一半也补上了——`tool_execution_update` 覆盖在调用行的尾巴上（粒度选"覆盖同一行"、250ms 节流、60 字折行与三处清空，见第 12 节第七点），管道那条纯文本路径不画它，因为那一路只往后追加。
5. **`outputSchema`。** 2025-06-18 加了 `structuredContent` 与 `outputSchema`，atlas 只有前一半：`Result.StructuredContent` 在，但 `wire.Tool` 没有 `outputSchema` 字段（`internal/wire/message.go:72`），所以模型看不到工具承诺的输出形状。加它要动契约层和三家适配器的转换，第一版只把 `structuredContent` 当文本给。

## 11. 落地时改了什么

写代码的过程推翻了原计划的七处，另有四处是计划里没提、实现时必须回答的；最后两条（十二、十三）是 0.12.11 那一轮的。记在这里是因为第 1 到 9 节写的是"打算怎么做"，而下面这些是"实际能怎么做"。

**一、`ping` 没有落地。** 第 9 节的"做"清单里有它，实现完发现没有消费者：握手已经证明过一次"服务器在"，而 `/mcp` 读的是启动时缓存的状态——那正是"我连上了什么"这个问题的答案，不是实时探活。一个没人调用的方法只能让人猜它什么时候该被调用。真正的探活需求是"服务器中途死了怎么办"，那要 `tools/list_changed` 加重连，都不在第一版。所以第 9 节的清单减一条，第 6 节的"工具调用不重试"照旧。

**二、SSE 答案流不能等到 EOF。** 第 4 节只说了"响应可能是单条 JSON，也可能是一段 SSE"，没说这条流什么时候结束。实测：Streamable HTTP 的答案流在发出响应之后并不关——它是双工传输的一半，服务器会一直挂着等后续消息。照"读完再解析"写，每个远端工具调用都会读到 60 秒超时才返回，而答案早就到了。所以 `decodeStream` 一拿到 `id` 对得上的响应就返回，不等 EOF，`TestStreamableHTTPSession` 的假服务器发完响应故意不关连接，钉的就是这条。代价：同一条流上排在响应之后的消息会被丢掉——第一版不处理服务端主动请求，丢掉的正好是不认识的东西。

**三、唯一的重试边界比第 6 节窄，也比它宽。** 第 6 节抄了 pi 的两句："工具调用不重试"、"`tools/list` 与资源读取可以重试"。实现下来只有一处重试：带 session 的 HTTP `404`（`errSessionExpired`）。它比那两句都窄（只有这一种错误会重试），又比"工具调用不重试"宽（`tools/call` 也走这条路）。理由是重试的唯一合法前提是**请求没有被派发**：服务器说它没有这个会话，意味着它没执行过那个调用。`500`、超时、连接断掉都不满足这个前提，一律不重试。`TestCallsAreNotRetried` 钉的是后一半：假服务器对第一次 `tools/call` 回 500，日志里 `tools/call` 恰好一次。

**四、`tools/list` 必须跟游标。** 第 9 节只写了 `tools/list`。规范允许分页，而只读第一页的客户端会静默丢工具——现象和"服务器本来就没有这些工具"一模一样，正是第 5 节最怕的那种混淆。加了 `nextCursor` 循环，上限 50 页：给到 50 页还有游标就报错，而不是转到天亮。

**五、`${NAME}` 未设时保留字面量并记名。** 第 7 节只写了"做 `${NAME}`"。展开成空串的后果是服务器拿着空 token 去认证，`401` 离原因隔着一整个服务器。所以未设的变量原样留在值里（服务器报错时那句话里就有变量名），同时 `MCPConfig.Missing` 记下名字，`cmd/atlas` 启动时打一行 `warning:` 指到文件。这也是 `os.ExpandEnv` 不能用的原因：它把"没设"和"设成空"变成同一件事。

**六、`sse` 搬家比"改 import 路径"大。** 第 4 节估的是"三家适配器加四个闸门脚本改一个 import 路径"。实际是 4 个 `.go`（三家适配器加上那个包自己的测试里的包路径）和 6 个闸门脚本，而且搬完第一次跑全量闸门就是红的：`internal/sse/sse_test.go` 用 `filepath.Join("..", "..", "..", "testdata", "sse", name)` 读 fixture，包深一层之后少一个 `..`。import 重写摸不到字符串里的相对路径，只有真跑测试才发现。教训：目录搬家的成本要按"这个包有没有用 `..` 找过外面的文件"算，不能按 import 行数算。

**七、假服务器用 `TestMain` 加重入，不用 `-test.run`。** 第 9 节写的是"测试二进制 `-test.run` 重入成子进程"。改成 `TestMain`：`-test.run` 需要一个专门阻塞的测试函数，而假服务器的退出码得是它自己的（`failed` 场景要退 1 并留 stderr）；`TestMain` 在 runner 之前跑，直接 `os.Exit(serveFake(scenario))`，场景名走环境变量，子进程的 stdout 里没有测试框架的输出，也不用为它写一个假测试。

计划里没提、实现时必须回答的四处：

**八、`/mcp` 的输出形状。** 第 9 节只写了 `mcpTools(cfg, cwd)`，没说这个命令。实现是两张表：`SERVER VERSION TOOLS STATE` 四列，加 `/mcp <server>` 的 `TOOL DECLARED AS DESCRIPTION` 三列（`DECLARED AS` 是交给模型的那个名字）。失败原因打在表下面而不是塞进 `STATE` 列——一条错误能有多长就多长，塞进表格会把读者要看的列挤到屏幕外。命令只读：能重连、能改文件都需要先给每个动词起个名字，而"我连上了什么"还没回答完。另外 footer 里每个服务器自成一列（`mcp:<server>`）：不分的话所有远端工具挤进同一个桶，而 footer 的用处正是"现在有哪些工具可用"；前缀 `mcp__` 从 `mcp.ToolPrefix` 导出，不让 `cmd/atlas` 再写一份字面量。

**九、连接期与调用期是两个超时。** 第 7 节的 `timeout` 是单次调用的上限（缺省 60 秒），只出现在 `tools/call` 上。启动时握手加列工具用的是另一个更短的 20 秒，而且所有服务器并行连：串行的话，配置里三台起不来的服务器会让 atlas 卡住一分钟才进提示符。失败按服务器拆开——配置文件读不懂是错误（启动失败），单台连不上只是一句 `warning:` 加一条 `/mcp` 里留着的 `failed` 行，因为那是这台机器的事实，只该少掉它自己的工具。

**十、工具名撞车不靠计数器。** 第 10 节第 3 条把这个列成"要不要在第一版就做"，实现时直接做了，理由在落地后更清楚：撞名只可能来自字符替换（`a.b` 与 `a_b`），而用 `_1` 这类计数器会让名字依赖"服务器这一轮回了哪些工具"——一台服务器挂掉重来，别的工具名字就变了，系统提示词跟着变。所以撞车用 `sha256(server\0tool)` 的前 8 位，名字只取决于这个工具从哪来。仍撞（两个不同工具哈希相同）才追加 `_`：唯一性优先于长度。

**十一、`type` 省略时靠字段推断，`-` 与 `_` 视为同名。** 第 7 节两条都写了，实现上各多一条：`type` 与字段冲突（写 `"type": "http"` 却给了 `command`）单独报错，而不是让其中一个赢；名字归一化把 `fs-server` 与 `fs_server` 判成同一台，否则配置里能有两条指向同一个东西的条目，而模型看到的是两套名字。

**十二、`tools/list_changed` 的接收点在连接层，而且必须 `go` 出去。** 通知到达的那条 goroutine 就是读连接的那条，在它里面发请求会等一个只有它自己能投递的答案——直接死锁（第 2 节的传输层模型放这里刚好：`Call` 是同步的，`sink` 是异步的）。重列也因此不能放在传输层：它要游标循环、白名单和跨服务器的名字分配，这三样都在 `Set` 手里，而传输层看到的是消息不是方法。名字分配特别值得一提——`assignNames` 一直是对所有服务器一起跑的，加工具时不能只对一台跑，否则新工具可能拿到别人已经在用的名字。

**十三、"下一轮出现"落在 registry，不落在 `agent.Config`。** 原本以为这里要动 app 的构造契约，实际不用：`app.Config.Tools` 从 0.12.6 起是 `ToolSet func(cwd)`，`agent.Config` 仍然不可变，缺的是 `agent.New` 里那一句 `tool.NewRegistry(cfg.Tools...)` 之后没有第二次。所以补的是 `Registry.Replace` 加 `App.Ask` 每轮重绑一次（`app.refreshTools`），和 `refreshPrompt` 并排——两处的理由本来就一样：会话跑着的时候，提示词和工具表都不是启动时那份了。整份替换而不是按名字比对，因为服务器可以用同一个名字换 schema，而 schema 正是模型读的东西。侧会话不跟，这是那边既有的快照规则：它的提示词与工具都停在打开那一刻。

## 12. 长任务：`GET` 流与 progress

第 4 节与第 10 节第 4 条留下的那条硬上限，在 OAuth 之后的一轮里补上了；界面那一半（进度画在调用行上）是再下一轮补的，记在第七点。这一节记的是补的过程里做的取舍，以及三处实测推翻的估计。

**一、`timeout` 是空闲上限，不是总时长。** 原来的语义是"这次调用最多花这么久"，新的是"这么久没有消息就放弃"。理由是客户端唯一分不清的东西：慢与卡住长得一模一样，而静默是两者的共同表现，服务器一报告进度就自证还在干活。没有加总时长上限——一个真的跑了十分钟的构建不该被杀，而人想停的时候 Esc 就在那里，多一条上限只会多一种"为什么它被杀了"的解释不了的情况。

**二、`context.WithTimeout` 换成 `context.WithCancel` 加 `time.AfterFunc`。** deadline 只能提前不能推后，而"推后"正是这个功能。每次 `notifications/progress` 重新 arm 一个定时器，并带一个 generation：被替换掉的定时器就算已经在回调里排队，也会因为 generation 不匹配而放弃——否则"刚报告完进度就被上一个定时器杀掉"会成为最难复现的一类 bug。arm 与登记在锁内，`tool.Partial` 的投递在锁外：界面允许慢，慢的重绘不该挡住下一条通知和马上就要到的答案。

**三、`_meta.progressToken` 每次都发。** 不是为了给人看：token 是"这条通知属于哪次调用"的钥匙，也是"慢还是卡住"的判据。客户端没有任何办法事先问服务器会不会报进度（规范里没有这个方法），所以不发就永远收不到。token 是计数器的字符串形式，比较时用 `fmt.Sprint` 归一——服务器可以把字符串 token 回显成数字，两种都得认，认不出就丢弃（一条通知掉在一个没人等它的调用上，比记错账好）。

**四、两条通道都读。** 进度可能内联在 `POST` 自己的答案流里（工具还在跑、连接还没关），也可能走 `GET`；服务器在这一点上不统一，而只接一条正是这类客户端最常见的错。所以 `decodeStream` 边读边分发（内联的那条不进返回的切片，它不是答案），`GET` 在握手之后开一次。

**五、`GET` 只开一次、不重连，而且它的失败全是静默的。** 405 是"这台服务器没有这条流"，401 是"token 在闲置时过期了"（下一次 `POST` 会顺手修好），坏事件只跳过、不停止读。这条路承载的全是"没有也能活"的信息，为它每台服务器每次会话打一行警告是噪音。代价写在这里：带 session 的 `404` 触发重建之后 `GET` 不会再开——`streamOnce` 只放一次，而旧流属于一个已经被服务器忘掉的会话，重连它等于对着一具尸体说话；那之后进度只剩 `POST` 内联那一条。

**六、放弃时交给模型的是可行动的那句话。** `<tool>: silent for 60s, so the call was abandoned; a server that reports progress restarts this clock, so raise this server's "timeout" only if it cannot`。判断"是不是我们主动放弃的"用的是 `callState.expired`，不是 `ctx.Err()`：子 ctx 被 cancel 而父 ctx 干净，`agent.execute` 因此把 `err.Error()` 而不是 `Operation aborted` 交给模型。stdio 顺带发一条 `notifications/cancelled`（服务器该停手了），HTTP 没有这条通道，放弃只能断掉连接。

三处实测推翻的估计：

**七、把 progress 接到 `tool.Partial` 不等于界面上看得见。** 计划只写到"接上 `tool.Partial`"，而那一环是通的：`agent.execute` 把每个 `Partial` 发成 `tool_execution_update`（`internal/agent/dispatch.go:176`）。但那一轮屏幕那头没有分支——`internal/tui/events.go`（现 `internal/transcript/event.go`）的 switch 与 `cmd/atlas/render.go` 都只认 `tool_execution_start` / `tool_execution_end`，所以当时只有 `--json` 与 `-rpc` 的消费者看得到，全屏会话里一个长任务仍然像卡住。那时没顺手补有两个理由：渲染粒度没定（一行覆盖一行，还是每分钟一行），以及那纯粹是界面侧的改动。**下一轮补上了，粒度选"覆盖同一行"**：块上多一栏 `Block.Progress`，与结果那栏 `Note` 分开——否则结果落地时 `endTool` 会把 `3/10` 和"42 output lines"拼在一起；节流 250ms（服务器按自己的节奏报，快的会比重绘还快，而每秒变四次已经够"活着"）；消息折成一行截到 60 字（尾巴按约定只有一行，带换行的 message 不能把调用撑成一块）；结果落地、并行调用关掉上一行、中断收尾这三处都把这一栏清空。没选"每分钟一行"是因为它会往 transcript 里加行：一个跑十分钟的构建就是十行只说明"还在跑"的噪音。

**八、`GET` 撞坏了一处既有的假服务器。** `TestRefreshOnce` 的 handler 把"非 `/token` 的请求"都当成 JSON-RPC `POST`，于是新的 `GET /mcp` 走进来把空 body 交给 `json.Unmarshal`，报 `unexpected end of JSON input`。改法是给那个 handler 加一条 `GET → 405` 的守卫，而不是让 `readStream` 少发一次请求：那个假设本来就只在测试里成立，而 `GET` 是规范允许的行为。`http_test.go` 里两条同形的 handler 提前加了同样的守卫（它们也是先把 body 读出来再分派），三处注释写的都是"这台假服务器没有流通道"。

**九、`httptest.Server.Close()` 会等那条 `GET`。** 新测试里 `t.Cleanup(server.Close)` 必须先注册、`t.Cleanup(set.Close)` 后注册：`Cleanup` 是后进先出，这样先关 `Set`（取消 `GET`）再关服务器；反过来的话 `Close` 会阻塞在一个还挂着的 `text/event-stream` 上，测试直接超时。handler 也必须 select `r.Context().Done()` 才能及时退出。

没做的三件同源，都写在这里而不是留着让人猜：不重连 `GET`（上面第五条）、不设总时长上限（第一条）、不把 `progress` / `total` 换算成百分比或 ETA（服务器的总量常常是阶段性的，算出来的百分比会先涨后跌，而一条一路向上的假进度比没有更糟——今天 `progressText` 优先用服务器自己那句 `message`，没有才退回 `3/10`，连这个都没有就只写数字）。
