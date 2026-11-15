# MCP 的 OAuth

MCP 客户端要连上"只给 OAuth"的远端服务器，就得自己走一遍授权码流程。这份文档有两半：第一到七节写于动手之前——规范要求什么、参考实现怎么做的、atlas 的决定是什么、凭什么验收；第八节留未解问题；第九节留给落地之后的记录。

标注约定：**【spec】** 从 MCP 规范与相关 RFC 原文读到的；**【pi】** 从 `packages/mcp/src/oauth/` 读到的；**【atlas】** 我们自己的决定。前两个都是"读到的"，不是"跑过的"——没有一台只支持 OAuth 的服务器可用于实测，所以凡是推断都标出来。

## 1. 触发这件事的两件事实

**第一，今天的 `401` 文案不可行动。** `internal/mcp/transport.go` 的那句是"把 token 放进 `headers`"，而它假定了服务器卖静态凭据。对只做 OAuth 的服务器（[`mcp.md`](mcp.md) 第 9 节点名的那一类），这句话是让人去做一件服务器不允许的事。

**第二，本地 CLI 只有 DCR 一条路。** 2025-11-25 那一版加了 CIMD（Client ID Metadata Document）：`client_id` 是一个 https URL，客户端托管一份描述自己的 JSON 文档。**【spec】** 这对 atlas 不可用——它没有域名。参考实现把它做成可选注入（`clientMetadataDocument`），对它有意义（有托管产品），对 atlas 没有。所以"要不要包含 DCR"不是偏好问题：不包含，能连的服务器只剩"允许预注册 client"的那一小撮。

顺带纠正一个容易混的地方：这不是订阅。provider 侧的 OAuth（`/login` 拿托管 token）才和计费有关，MCP 侧这一个是纯粹的授权——服务器在问"这个客户端能不能代表你访问你的数据"。

## 2. 规范的形状

**【spec】** 七份 RFC 加一份 MCP 规范，全部围绕"客户端没有预先注册、用户点一下浏览器就完成"这一件事：9728（PRM）、8414（AS 元数据）、7591（动态注册）、7636（PKCE）、8707（resource）、9207（`iss`）、8252（原生应用的回调）。

### 2.1 一次 401 带回来的三样东西

服务器拒掉请求时，`WWW-Authenticate` 头里带：

| 参数 | 用途 |
|---|---|
| `resource_metadata` | PRM 文档的地址。**这是唯一能拿到它的场合**——没有这个头就只能猜 well-known 路径 |
| `scope` | 这次要的权限范围 |
| `error` / `error_description` | `insufficient_scope` 表示"授权了但不够"，其余是"没授权" |

**【spec】** 解析时 scheme 只认 `bearer` 与 `dpop`，其它一律当作没有这个头；空值（`scope=""`）等于没写——它不携带信息，当成一个空 scope 会让请求带着一个空字符串出门。**【pi】** 它的 `parseWwwAuthenticate` 就是这么做的，且用正则同时接受带引号与不带引号两种形状。

### 2.2 两级发现

**第一级：PRM（RFC 9728）。** 路径是 `/.well-known/oauth-protected-resource` **加服务器路径的后缀**：服务器在 `https://example.com/mcp`，文档就在 `https://example.com/.well-known/oauth-protected-resource/mcp`。**【pi】** 带后缀的那条 4xx（或 502）时，退回不带后缀的那条再试一次；两条都"不在"才算失败。这个"4xx 与 502 等于不在这里"的判定很重要：服务器对不认识的路径回 404 是常态，把它当错误会让一半的服务器连不上。

文档给出 `resource`（服务器自己的规范 URL）、`authorization_servers[]`（谁发 token）、`scopes_supported[]`。

**第二级：AS metadata（RFC 8414 或 OIDC）。** 候选 URL 有三个，按序试：

1. `/.well-known/oauth-authorization-server` + issuer 路径后缀
2. `/.well-known/openid-configuration` + issuer 路径后缀
3. `<issuer 路径>/.well-known/openid-configuration`（路径插在中间，OIDC 的老形状）

**【pi】** 三条都试，命中即止。**紧随其后的是 issuer 校验**：文档里的 `issuer` 字段必须等于用来发现它的那个 URL（尾斜杠不作数）。这不是参考实现的洁癖——RFC 8414 §3.3 与 MCP 规范都要求它；不等就是"这份文档不是这台 AS 的"，用它注册等于把 client 交给了一个中间人。

### 2.3 注册（DCR，RFC 7591）

`POST registration_endpoint`，请求体里这几项是必填的：

```json
{
  "redirect_uris": ["http://127.0.0.1:53211/callback"],
  "grant_types": ["authorization_code", "refresh_token"],
  "response_types": ["code"],
  "token_endpoint_auth_method": "none",
  "application_type": "native"
}
```

**【spec】** `application_type` 不在 RFC 7591 里，它是 OIDC 的注册扩展。**不写它的后果是具体的**：OIDC 服务器默认按 `web` 处理，而 `web` 客户端不许用 http 回环地址，于是注册成功、授权时才失败，错误还指向 redirect_uri。**【pi】** 它按 `redirect_uris` 算这个字段（有回环或自定义 scheme 就是 `native`），SEP-837 记的就是这件事。

### 2.4 授权码与 PKCE

构造 authorize URL：`response_type=code`、`client_id`、`code_challenge` + `code_challenge_method=S256`、`redirect_uri`、`state`、`scope`、`resource`。

**【spec】** PKCE 是必需的（RFC 7636），`S256` 是唯一可接受的算法：AS metadata 声明了 `code_challenge_methods_supported` 却不含 `S256` 时应当直接报错，而不是退回 `plain`。`resource`（RFC 8707）在 authorize 与 token 两个请求上都要发。

**【pi】** 一个细节：scope 里含 `offline_access` 时额外发 `prompt=consent`，因为不重新征得同意就拿不到 refresh token。这条不显然，但它决定"第二次启动还要不要再点一次浏览器"。

### 2.5 token 与刷新

`POST token_endpoint`，`application/x-www-form-urlencoded`：

- `grant_type=authorization_code` + `code` + `code_verifier` + `redirect_uri`
- `grant_type=refresh_token` + `refresh_token`

**【pi】** 两处错误处理值得抄：**OAuth 的错误可能配任何 HTTP 状态码**（有的服务器用 200 返回 `{"error": ...}`），所以先看 body 里有没有 `error` 再看状态码；刷新时如果响应没带 `scope`，要沿用旧 token 的 scope（RFC 6749 §6），否则一个"没声明变化"的刷新会把权限悄悄缩掉。

### 2.6 四个必须校验的东西

| 校验 | 不做的后果 |
|---|---|
| AS metadata 的 `issuer` 等于发现它的 URL | client 凭据交给替身 AS |
| 授权响应的 `iss`（RFC 9207）等于 issuer | 一台 AS 的 code 换到另一台的 token（token 混淆） |
| 回调的 `state` 等于自己生成的那个 | 别人构造的回调被当成授权结果 |
| PRM 的 `resource` 与服务器 URL 同源且同前缀 | 拿到一枚受众是另一台资源的 token |

**【pi】** 前两条它都做，`iss` 那一条在没有 AS metadata 时**拒绝任何** `iss`（没有可比的 issuer，就不接受"我是谁"的自述）。第三、四条在 `selectResource` 与 callback server 里。

## 3. 参考实现读到了什么

`packages/mcp/src/oauth/`：callback 164、discovery 215、errors 55、flow 482、index 56、provider 168、types 209 = **1349 行**，外加 `auth-provider.ts` 那个 20 行的接缝（`token()` + `onUnauthorized(context)`）。文件头写着它改编自 `modelcontextprotocol/typescript-sdk v1.29.0 src/client/auth.ts`（MIT）——也就是说这是官方 SDK 的形状，不是某家的私有做法。

五条不显然、且决定实现复杂度的：

1. **发现状态缓存在磁盘上。** 一次登录要 3~5 个网络往返，`discoveryState` 把它存下来，下次直接从缓存的 issuer 取 token 端点。**【atlas】** 只缓存刷新需要的三项（`authorizationServer`、`tokenEndpoint`、`resource`），不缓存整份文档——理由是刷新的失败路径本来就要重新发现一次，多存字段只是多一个过期的机会。
2. **刷新先于跳转。** `runFlow` 在有 refresh token 时先刷新，成功就返回 `AUTHORIZED`，根本不进浏览器。这是"第二次启动不该再点一次"的实现处。
3. **`invalid_client` / `invalid_grant` 各重跑一次。** 前者作废全部凭据（AS 删了我们的注册），后者只作废 token（refresh chain 被轮换掉了）。**【pi】** 只重跑一次，不递归——否则一台坏掉的服务器会变成一个循环。
4. **并发 401 共享一次刷新。** 并且刷新之前先比对"当前 token 还是不是刚才被拒的那一枚"，是的话才刷：refresh token 轮换时，第二次用旧 refresh token 刷新会失败，并把刚拿到的新授权一起作废。
5. **`insufficient_scope` 是升级授权，不是重新授权。** 挑战里的 scope 只是缺的那部分，要跟已授予的合并（SEP-2350），否则升级一次反而丢了原来的权限。

另外两处工程细节：回调服务器绑定 `127.0.0.1` 的随机端口、`state` 与等待方一一对应、默认 5 分钟超时、关闭时把所有等待方拒掉；存储层 `own()` 用 `serverUrl` 过滤——**读到的状态属于另一个 URL 就当没有**，这条是"凭据不跨服务器泄漏"的唯一保障。

## 4. 【atlas】怎么接

### 4.1 配置面

`mcp.json` 的服务器条目加两个键：

```json
{
  "mcpServers": {
    "linear": {
      "type": "http",
      "url": "https://mcp.linear.app/mcp",
      "auth": "oauth",
      "scopes": ["read", "write"]
    }
  }
}
```

四条校验，都是"写在文件里就该报错"的那一类：

- `auth` 只接受 `"oauth"`，其余值报错并列出可接受的值。
- `auth: "oauth"` 必须配 `url`：stdio 服务器没有 HTTP 端点，OAuth 无处安放。
- `auth: "oauth"` 与 `headers.Authorization` 互斥。同时写意味着两个凭据来源，选哪一个都是猜。
- 项目级（`<cwd>/.atlas/mcp.json`）出现 `auth` 或 `scopes` 是错误，与 `headers` 同一条理由：一个能加授权意图的项目文件是仓库里的代码执行。

**为什么不自动试探。** 401 之后自动进入 OAuth 意味着客户端会向 metadata 端点、注册端点、浏览器发起请求，而"这台服务器要 OAuth"是用户可以声明的意图。atlas 的既有风格是显式（`-demo` 宁可报错也不忽略别的一半），这里照办。

### 4.2 启动路径：没 token 就不连

`mcpFor` 在启动时并行连接所有服务器。带 `auth: "oauth"` 且存储里没有 token 的那一台，**不发请求**，直接记成 `needs login`，`/mcp` 的表格里 `STATE` 列就是这样一行。

理由是启动路径上一次 401 除了把"你还没登录"说得更慢之外没有别的信息，而它会把并行连接的一格变成一次没必要的往返。真正需要登录的人会去跑 `/mcp login`，那是唯一能让它变好的动作。

### 4.3 `/mcp login <server>`

`/mcp` 今天是只读的，`cmd/atlas/mcp.go` 的注释写着理由（它回答"我连上了什么"）。加 `login` 是有意推翻这一条：**"needs login" 这个状态之后的唯一动作就是登录**，一个只能报告状态、不能改变状态的命令会让读者去找 `mcp.json` 之外的第二份文档。

七步：

1. 从配置取 URL；服务器不存在或不是 `auth: oauth` → 报错并指出是哪一种。
2. 发一次 `initialize`，**只为了拿 401 上的 `WWW-Authenticate`**——`resource_metadata` 只在那里出现。
3. 发现 PRM（没有第 2 步的地址就退回 well-known 加后缀的那条），校验 `resource`。
4. 发现 AS metadata，校验 `issuer`。
5. 取存储里的 client；没有就 DCR 注册一个并落盘。
6. 生成 PKCE 与 `state`，打印 authorize URL，等回调。
7. 换 token、落盘、打印服务器名与 scope。

**第 6 步有个粗糙的地方，写在这里而不是藏起来。** 回调要等一个人点浏览器，最长 5 分钟；而 `runTUICommand` 是同步的，就地等会把整个界面冻住（连 Ctrl+C 都进不来）。所以等待放在一个 goroutine 里，命令立刻返回一句"等待浏览器"，结束（成功、失败、超时）时由那条 goroutine 写一行 note。代价是**这一版取消不了等待**：Ctrl+C 在空闲状态下管的是输入行与退出，管不到那条 goroutine，只能等它自己超时。修它是给 `Sink` 加一个可取消的等待状态，属于新的界面能力，不在这一版。

### 4.4 落盘

`$ATLAS_HOME/oauth.json`，权限 `0600`，同目录 tmp 加 rename 原子写。它会在两轮对话之间被改写，而写一半的 JSON 会让下一次启动丢掉全部授权。

按 **server URL** 索引，不按服务器名：改名不该丢授权，两台同名不同 URL 不该共享 token。存储形状：

```json
{
  "version": 1,
  "servers": {
    "https://mcp.linear.app/mcp": {
      "client": {"id": "...", "secret": "...", "method": "none"},
      "tokens": {"access": "...", "refresh": "...", "expiresAt": "2026-11-14T12:00:00Z", "scope": "read write"},
      "endpoint": {"authorizationServer": "...", "token": "...", "resource": "..."}
    }
  }
}
```

PKCE verifier 与 `state` **不落盘**：回调必须到达一个正在监听的 socket，所以等的人一定还活着；落盘只会让一次崩溃留下一个能被别人用的 verifier。

### 4.5 接缝

```
config.MCPServer.Auth / .Scopes
        │  cmd/atlas/mcp.go 翻译
        ▼
mcp.Spec.OAuth *mcp.OAuthSpec ──► httpTransport
                                     │ 每次请求：附上 token()
                                     │ 401：refresh() 一次 → 重试一次
                                     ▼
                               mcp.OAuthStore（oauth.json）
```

- `httpTransport` 在 `exchange` 里已经有 `t.mu` 保护 `session` / `version` 的读写，token 与刷新状态放同一处，并发 401 的串行化不需要第二把锁。
- `Spec.Headers` 原样保留：OAuth 只是"没有 `Authorization` 时补一枚"的来源，不是替代品。
- `mcp` 包仍然只依赖标准库加 `tool` / `wire` / `sse`（`net/http`、`net`、`crypto/sha256`、`crypto/rand`、`encoding/base64` 都不越界），`tests/mcp/run.sh` 的闭包不用改。

### 4.6 文件与规模

| 文件 | 职责 | 预估 |
|---|---|---|
| `internal/mcp/discovery.go` | 两条 well-known 的候选与"不在"判定、PRM 与 AS metadata 的解析、issuer 与 resource 校验 | 260 |
| `internal/mcp/oauth.go` | 注册、PKCE、authorize URL、token 交换与刷新、`OAuthSpec` 与错误分类 | 380 |
| `internal/mcp/callback.go` | 回环 listener、`state` 绑定、超时、浏览器那一页的纯文本 | 170 |
| `internal/mcp/store.go` | `oauth.json` 的读写、0600、原子写、按 URL 取用 | 140 |
| `internal/mcp/set.go` | 带 `OAuth` 而无 token 的 spec 不连接，但仍然出现在 `Servers()` 里并带一个 `needs login` 状态 | +50 |
| `internal/config/mcp.go` | `auth` / `scopes` 两个键与四条校验 | +60 |
| `cmd/atlas/mcp.go` | `/mcp login` 的解析、登录协程与它结束时那行 note、表格的 `needs login` 一格 | +90 |

合计约 1130 行加同量测试，跨 6 个文件，全部在标准库内。

## 5. 第一版不做

1. **`client_secret_post`。** 只做 `none` 与 `client_secret_basic`。前者是 DCR 的 public client，后者是行业默认；中间那一种只多一条路径，遇到时错误里说明"这台服务器只支持把 secret 放进 body，atlas 不做"。
2. **step-up 授权（`insufficient_scope`）。** 第一版报"这台服务器的授权少了 scope，删掉 `oauth.json` 里那一项再登录一次"。合并已授予与挑战 scope 需要把每次的授予记下来，而 token 响应可以不带 `scope`——一个记不准的合并比重新登录更糟。
3. **CIMD**（第 1 节）。
4. **设备码流（RFC 8628）。** 它是 SSH / 容器场景的正解，见第 8 节第 1 条，但它是第二条授权路径。
5. **自动开浏览器。** `mcp` 包只把 URL 交出去，`cmd/atlas` 打印它。单二进制不该在任何平台猜 `open` / `xdg-open` / `start`。
6. **后台刷新。** 没有定时器：token 过期由 401 触发一次刷新，刷新失败就报"再登录一次"。
7. **token 的轮换通知与多进程协调**（第 8 节第 5 条）。

## 6. 一个会让人卡住的后果

`needs login` 的服务器在启动时一个请求都没发，所以它根本没有连接（[`mcp.md`](mcp.md) 第 5 节）。连接是进程启动时的一次性事务，而登录发生在它之后，所以**登录之后，那台服务器的工具要重开 atlas 才出现**——不是重开会话：工具树是进程级的，换一个会话只是换一份对话，同一个进程里的工具树还是原来那棵。落地时这句话就按这个口径写进了报告（`its tools appear after you restart atlas`）。0.12.11 之后工具表本身会在会话中途重列（[`mcp.md`](mcp.md) 第 5 节），但那条路走的是「已经连着的那台又发了通知」，而这台连连接都还没有。

这句话必须写在 `/mcp login` 成功的报告里，否则读者会以为登录失败了：

```
logged in to linear (scopes: read write)
its tools appear after you restart atlas
```

一个"成功"之后还要求第二个动作的报告是不体面的，但另一个选择——重建工具树——要动 `app` 的构造契约，那是比这次登录大得多的一件事。

## 7. 验收

每条都是一条会变红的测试，跑在 `tests/mcp/run.sh` 里。假 AS 用 `httptest` 起，回调测试真监听 `127.0.0.1` 再由测试自己去 GET 它。

1. **PRM 发现**：401 带 `resource_metadata` 时用它；不带时退回带后缀的 well-known；带后缀那条 404 时退回根路径；两条都 404 时报错并带上试过的两个 URL。→ `TestProtectedResourceMetadata`
2. **issuer 校验**：AS metadata 的 `issuer` 与发现它的 URL 不同时报错，且**没有发出注册请求**。→ `TestIssuerMismatchStopsBeforeRegistering`
3. **注册**：请求体带 `application_type: "native"`、`grant_types`、`redirect_uris`；返回的 `client_id` 落盘；第二次发现已存 client，不再注册。→ `TestRegistersOnce`
4. **PKCE**：authorize URL 带 `code_challenge_method=S256` 与 `resource`；token 请求带 `code_verifier`；AS 只声明 `plain` 时报错。→ `TestPKCE`
5. **回调**：错误的 `state` 得到 400 且不换 token；正确时换到 token 并回一行可关闭的页面；超时后等待方拿到错误。→ `TestCallbackState`
6. **刷新**：401 后刷新一次并重试一次成功；刷新回 `invalid_grant` 时报"再登录一次"且只尝试一次。→ `TestRefreshOnce`
7. **落盘**：`oauth.json` 权限 `0600`；两个 URL 各自独立；服务器改名后 token 仍可用；半截 JSON 被当成空表而不是启动失败。→ `TestOAuthStore`
8. **启动**：`auth: oauth` 且无 token 的服务器零请求、状态 `needs login`、会话照常开始、工具列表里没有它。→ `TestOAuthServerWithoutTokenDoesNotConnect`
9. **配置**：项目级写 `auth` 报错；`auth: oauth` 与 `headers.Authorization` 同时写报错；`auth: "x"` 报错；`auth: oauth` 没写 `url` 报错。→ `TestConfigRefusesOAuth`

## 8. 未解问题

1. **SSH 与容器。** 回调落在运行 atlas 的那台机器上，浏览器在用户的笔记本上。标准解法是用户自己 `ssh -L 53211:127.0.0.1:53211`，但端口是随机的，要连就得先知道它——所以 `/mcp login` 打印 URL 时必须同时打印端口。真正的解是设备码流（第 5 节第 4 条）。第一版只能把这一步写在提示里。
2. **`authorization_servers[]` 有多个时取第一个。** **【pi】** 就是取 `[0]`。第一个不可用就整条失败，没有一个"按 `issuer` 挑一个"的规则——因为没有任何依据挑。
3. **服务器不给 `expires_in`。** 没有过期信息，`expiresAt` 只能空着，全部靠 401 触发刷新。这条路是通的，但每次启动的第一个请求会多一个往返。
4. **DCR 注册被 AS 删掉。** 表现为 `invalid_client`，照参考实现作废重注册一次。如果原因是权限（不该注册的 AS），这一次会失败并报出 AS 的原话——可接受，但错误里要带上"我们刚刚重新注册过"，否则读者会以为第一次就是这个错。
5. **refresh token 轮换与多进程。** 同时开两个 atlas 会让后刷新的那个把先刷新的那个作废。单进程串行刷新没问题（`httpTransport` 一把锁），多进程要的是文件锁加版本号，不在这一版。
6. **登录之后要重开**（第 6 节）。
7. **`/mcp login` 在侧会话与归档会话里的位置。** 侧会话只跑六条命令，第一版不把它加进去（一次登录会改磁盘上的共享状态，而侧会话的定义是"不碰主对话之外的东西"）；归档会话要拒，理由与 `/skill` 的加载相同——它在 append 之前就该拒绝。

## 9. 落地时改了什么

第 1 到 7 节写的是"打算怎么做"。落地后实际是：四个新文件 `discovery.go` 387、`oauth.go` 683、`callback.go` 184、`store.go` 187（计划 260 / 380 / 170 / 140，合计 1441 对 950），改动 371 行（`http.go` 143、`cmd/atlas/mcp.go` 116、`internal/config/mcp.go` 55、`set.go` 22、`repl.go` 25、`spec.go` 6、`transport.go` 4），测试 1305 行。超出估算最多的是 `oauth.go`：计划把它当成"五个请求加两个 helper"，实际它承载了整个流程的顺序、七处拒绝的文案、以及 token 端点那三种认证方式的分类。九条验收各一条测试全部落地并通过（`tests/mcp/run.sh`、`tests/config/run.sh`、`tests/cli/run.sh`）。

**一、TUI 会取消那一轮的 ctx，登录协程必须自己脱钩。** 计划里写的是"等待放在 goroutine 里，命令立刻返回"，但没写这一条：`internal/tui/model.go` 在 `Run` 返回后紧接着 `cancel()`，于是 `replCommand` 收到的 ctx 在 `/mcp login` 打印完第一行时就已经死了，`Login` 里的第一次 `probeServer` 会立刻拿到 `context canceled`。修法是 `mcp.Login(context.WithoutCancel(ctx), ...)`——这句话同时也是文档第 4.3 节那条"这一版取消不了等待"的代码形状：等待不该继承一个必然触发的取消，它只受 `LoginTimeout` 约束。这一处不改，"命令立刻返回"就变成了"命令立刻返回并立刻失败"。

**二、五条配置校验，不是四条。** 第 4.1 节列了四条，落地时加了第五条：`scopes` 脱离 `auth` 报错。理由与前面四条同源——一个被静默忽略的键正是这个文件存在的意义所在，而 `scopes` 不带 `auth` 恰好是最容易被忽略成"我写了但没生效"的那一种。测试 `TestConfigRefusesOAuth` 因此有八个子用例，不是四个。

**三、`Login` 不做"刷新先于跳转"。** 第 3 节第 2 点记了参考实现在有 refresh token 时先刷新、成功就不进浏览器。这里不做，因为 atlas 的 `Login` 只在一条路上被调用：用户主动敲 `/mcp login`。有 token 却过期或被撤的场景由 401 那条路处理（`reauthorize` 刷一次、失败就报"再登录一次"），而一个"我要求你登录，你却什么都不做就返回成功"的命令比多点一次浏览器更难解释。`invalid_client` 的重注册重试保留了：它不是"先刷新"，而是"这次交换还能用，只是对面不认我们的注册"。

**四、回调那一页是纯文本，`version: 1` 现在就写。** 第 9 节原来预计这两处最可能被推翻，实际两处都按原计划落地。纯文本在终端浏览器里可读，而这一页只有一句话要传达；`version: 1` 是给第二个版本留的判断点，一个从没声明过 schema 的文件在需要迁移时没有任何依据可循。

**五、注册与回调端口：沿用已存 client，依赖 RFC 8252 §7.3。** 这是第 2 节与第 4.4 节之间一处落地才显形的依赖：`clientFor` 找到已存 client 就不再注册，而每次登录的回调端口是新的随机端口，所以 AS 必须接受"注册时写了一个回环端口、授权时用了另一个"。RFC 8252 §7.3 要求的正是这件事（原生应用的回调端口不得参与注册比对），所有按 OIDC 原生客户端做的 AS 都满足。不满足的那一类会以 `invalid_client` 或一句关于 redirect_uri 的拒绝出现，`invalid_client` 那条已被重注册覆盖，redirect_uri 那条会原样报给用户。

**六、`401` 与 `403` 在 OAuth 情形下是同一件事。** `http.go` 的 `read` 把两者都归到 `errReauthorize`（`docs/mcp-oauth.md` 第 5 节第 2 条：step-up 不做）。这比计划里那句"403 也归到这里"更窄一点：非 OAuth 服务器的 403 仍然走 `errUnauthorized`，提示 `headers` 与 `auth: "oauth"` 两条路。

**七、`/mcp login` 在两种状态里被拒，与计划一致但位置不同。** 第 8 节第 7 条说侧会话不加这条命令、归档会话要拒。实际是两处都在 `repl.go` 的 `/mcp` 分支里显式拒（`side.typingInSide()` 与 `app.ErrArchived`），而不是靠 `sideCommands` / `archivedCommands` 两张表：`/mcp` 本身在两张表里都在，拒的只是它的 `login` 这一半，而命令表是按命令粒度分的，拆不出子命令。帮助文本因此仍写 `/mcp`，读者要试才知道 `login` 在这里不跑——这是这一版接受的粗糙处。

**八、`Notes()` 与表格是两套文案，`needs login` 各有一句。** 启动时 `mcp: server %q needs a login before its tools can be used; run "/mcp login %s"`，表格下面 `/mcp login <name>` 那句同样带命令。理由与失败那行写在表下相同：`STATE` 列只有两个词的宽度，而"下一步是什么"必须能被复制。
