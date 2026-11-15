# pi-go

pi agent 内核的 Go 实现。目标是一个静态单二进制、零运行时依赖的 agent 服务，同时保持与 pi 的 JSON 线格式和 JSONL RPC 兼容。

当前是 **内核 + 模型接入 + 工具集 + 会话存储 + 压缩 + 编排 + JSONL 传输 + 可运行的 CLI**：消息契约、流式装配、agent 循环、工具分发、SSE 读取、三家模型协议的请求转换与流式解码（chat completions、messages 与 responses）、六个文件工具（read/write/edit/ls/find/grep）、bash 执行（可选地套进 macOS 沙箱）、四个只读的 git 工具（`status` / `diff` / `log` / `show`，只读靠一张 argv 白名单构造而不是靠承诺——会执行外部程序的选项被单独拒绝并各带一句理由，`--` 之后一律当路径）、MCP 客户端（stdio 与 Streamable HTTP 两条传输，远端工具原样注册成本地工具，`GET` 长连流上的 `notifications/progress` 会把这次调用的空闲超时往后推，OAuth 那一路自己走完发现、动态注册、PKCE 与刷新）、v3 JSONL 会话（读写、续跑、分支、fork、列表）、上下文压缩（估算、切割点、split turn 双摘要、增量摘要、手动与阈值触发、发请求前的准入校验、请求超限时的一次压缩加重试）、会话与 agent 的编排（逐条落盘、模型与思考级别切换、steering、临时侧会话、fork 与改名、按文件换会话、`/export` 的三种导出格式）、大输出的落盘（bash、MCP 与 `webfetch` 超过上限时把全文写进会话目录并回一行路径，那条路径 `read` 直接读得回来）、插件目录（`$ATLAS_HOME/plugins/<name>/plugin.json` 一个目录声明一组 skill 与几台 MCP 服务器，工具进同一张表、同一套审批，`/plugin` 只读列出各自贡献；市场与安装不做）、兼容 `pi --mode rpc` 的 JSONL stdin/stdout 传输全部可运行。0.12.23 起打开 atlas 时匿名检查 GitHub release（公开仓库 `youverus/atlas`，10 秒超时、失败静默），有新版在首页提示，`/update` 手动安装、重启生效。`atlas` 是唯一的二进制：从 `~/.atlas/config.json` 读模型列表、端点与凭据，首次运行自动建目录与模板，认识终端（单次 prompt / stdin / 全屏会话 / JSONL / RPC），终端上默认进全屏会话（alt-screen、自有 transcript、审批弹窗、侧会话视图、段折叠与拖选复制），`-demo` 保留不联网的脚本化冒烟。测试全部离线：`httptest` 回放 fixture、临时目录跑真实文件与真实 bash、`faux` 回放 turn 与摘要、`io.Pipe` 驱动真实的 RPC 循环，不需要 API key。

0.4.0 起交互层只有一个形态：全屏 TUI，用 `charm.land/bubbletea/v2` 与 `charm.land/lipgloss/v2` 两个模块，一起打进二进制（0.12.4 起回答的 markdown 由 `charm.land/glamour/v2` 画，第三个模块）。行级 REPL 与它依赖的 `internal/editor`、`internal/term` 一并删掉——两套界面长期并行维护，每加一条行为都要在两处对得上，而两者对"屏幕上现在有什么"的假设并不一样。内核不跟着变：`internal/` 下除 `internal/tui` 之外的每个包仍然只用标准库，由 `tests/deps/run.sh` 机械检查——第三方依赖的准入是按层划的，不是按需划的。

文档：
- [docs/INDEX.md](docs/INDEX.md) — 目录索引，什么代码在哪里
- [docs/architecture.md](docs/architecture.md) — 分层约束、事件时序、与 pi 的差异

两份文档都随代码同步更新。

## 快速开始

```bash
# 构建需要 Go 1.26 以上（bubbletea v2 的下限）。首次拉依赖要能到模块代理：
# 国内直连 sum.golang.org 会超时，goproxy.cn 一并代理了校验库端点。
export GOPROXY=https://goproxy.cn
export GOSUMDB="sum.golang.org https://goproxy.cn/sumdb/sum.golang.org"

cd go
./tests/run.sh                         # 模块闸门：格式 / vet / test / race
./tests/run.sh sse                     # 只跑一个模块
go run ./cmd/atlas -demo               # 不读配置、不联网的冒烟
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o atlas ./cmd/atlas
cp atlas ~/.local/bin/atlas            # ~/.local/bin 已在 PATH，之后直接敲 atlas
atlas                                  # 首次运行：建 ~/.atlas 并写模板后退出
vim ~/.atlas/config.json               # 填一个模型和一个凭据
atlas "解释一下 internal/app"           # 单次 prompt（纯文本输出，不画界面）
atlas                                  # 终端里进入全屏会话
echo "看下 git status" | atlas          # 管道：stdin 就是 prompt
atlas -json "hi"                       # JSONL 事件
atlas -rpc                             # 在 stdin/stdout 上服务 JSONL RPC
atlas -list-models                     # 列出配置里的模型
```

终端里（全屏会话，alt-screen）：

```
│ 解释一下 internal/app 的重试循环

╭ Thought for 6.4s ────────────────────────────────────────────────────╮
│ the retry wrapper is the only place that sleeps                      │
╰──────────────────────────────────────────────────────────────────────╯

╭ 2 calls · read, grep ────────────────────────────────────────────────╮
│ read  {"path":"internal/app/run.go"} · 180 output lines              │
│ grep  {"pattern":"retry"} · 6 matches                                │
╰──────────────────────────────────────────────────────────────────────╯

│ 重试在 provider 层之上、turn 循环之内……

╭──────────────────────────────────────────────────────────────────────╮
│ > _                                                                  │
╰──────────────────────────────────────────────────────────────────────╯
  …/pi-agent-go/go | #01a16e7d | deepseek-chat | ctx 8.2k left
```

上面是 transcript，中间空一行再是输入行（这行空白是故意的：回答的最后一行与正在打的那一行贴在一起时，一段折过行的回答读起来像"它最后一行就是提示"，所以 `blocks.composer.gapAbove` 缺省留一行、也可以自己改（`tui.json`），命令面板打开时空白仍在面板之上——面板属于正在打的那一行），最下面是状态栏。0.4.15 起整块屏幕刷主题的底色，输入行外面套一圈圆角框、框里再亮一档（`blocks.composer.frame`，`false` 就回到没有框的样子），这样"我要打字的地方"在满屏同色的正文里有一个能一眼找到的边界。0.4.18 起对话里的**段**也有自己的框：一段思考、一段连续的工具调用各自装进一个盒子，标题嵌在顶线上（`Thought for 6.4s`、`2 calls · read, grep`），正文逐行补到右边框。要的是一眼的形状——一轮就是"想一段、调一批、再想一段"，可这块信息原来只躲在每块第一行的字形里，一段跑过二十行之后段边界早就滚出视野了。现在一段两条框线，段里不再有 `  ├ • ` 那枚枝桠（框本身就是分组），代价是每个盒子两行，所以它是开关：`blocks.transcript.frames` 写 `false` 就退回 0.4.17 的一行一块（逐字节相同）。刚进来时那张是欢迎页（字标、版本、目录、模型），输入行里打 `/` 会在它上面弹出命令面板，右边一句话说明每一个命令；`/model` 与 `/resume` 不带参数则弹出一张可选列表（`↑`/`↓` 挑、`←`/`→` 转这一行的思考档位、`Enter` 取走、`Esc` 走开）。`Ctrl-O` 切侧对话，`Ctrl-C` 连按两下中断一轮或（空行上按两下）退出，`/help` 列命令，键位全表见下面「全屏会话」。

输出示例（`-demo`，不联网）：

```
  ├ • Ran  echo  {"text":"hello from atlas"} · 1 output line

● demo complete
[stop reason stop]
```

纯文本路径（`-prompt`、管道、`-demo`）把一轮 turn 画成一棵树：回答是页边距上的实心点 `● `，模型做的事都挂在往内两列的枝上 `  ├ • `。工具调用开始时就把 `Ran  名字  参数` 打出来（不带换行），结束时在同一行补上产出与耗时：`· 3 output lines`、`· failed`（失败的结果正文缩进 4 空格跟在下面）、耗时满 1 秒才写（`· 1.2s`，更快的调用写出来只是噪声）。思考块在结束时汇报时长 `Thought for 6.4s`（不足 1 秒写 `840ms`），压缩是 `Compacting  原因` 与 `Compacted  N → M tokens`。文本与枝之间空一行，枝与枝连着不空行；多行回答的续行缩进两格对齐在 `● ` 下面。turn 结束的收据打到 stderr，所以 `atlas -prompt ... > log` 的 stdout 只有回答与这棵树：`[1723 tokens (in 1204, out 519), 62 tok/s, took 8.4s, ended at 15:03:11, stop reason stop]`。`tok/s` 的分母是模型写字的时间（跑工具那几秒不算），`took` 是这一轮的墙钟——两者接近就说明这一轮几乎没调工具，差得远就说明时间花在了别处；没花 token 的轮只留 `[stop reason ...]` 一句，因为这一行本来就是「花了多少」的账，没花就没有账。每条枝都用同一个 `├`：并行调用默认开着，任何一行都可能是最后一行，而终端不会让你回头改已经滚过去的那一行。全屏会话讲的是同一件事，只是它有整屏可以重画，形状刻意不同：管道里要的是一份能 grep、能粘进工单的日志，屏幕上要的是一眼能看出结构的东西（下面那两段）。两边共用的是**用语**——`Thought for 6.4s`、`· 3 output lines`、`· interrupted` 在两种界面上逐字相同，所以从日志切到屏幕不用重学一套读法。

**全屏会话里回答换了字形**：不再只在第一行放一枚 `●`，而是每一行都以一条竖线 `│ ` 起头（竖线用强调色，正文用正文色）。理由是一段回答会折成二十行、滚出半屏、而且还在一个个 token 地写进来：读第二屏的时候，第一行那枚点早就不在视野里了，而一条贯穿整块的边在任何滚动位置都还在；它也不靠亮度（加粗在深色终端上并不可靠）。用户行 `> ` 与提示、错误的形状一个字未改；工具与思考在 0.4.18 换成了段落盒子（见上面那段示例）——它们本来就短、数量多、靠字形分得开，缺的只是"从哪儿到哪儿是一段"这条边界，而那正是框能给、字形给不了的东西。纯文本路径保持那棵树：它还要区分主侧对话（`●` / `○`），一条竖线做不到这件事。`/resume` 重放历史时因此不走逐行的提示：答案原文交回界面（`tui.Sink.Answer`）画成同一个带竖条的块，同一屏上不会出现两种回答。

## 目录

完整索引（每个目录的职责、主要文件、状态）见 [docs/INDEX.md](docs/INDEX.md)。依赖方向速览：

```
cmd/atlas ─┬─> internal/config ─> (仅标准库)
           ├─> internal/app ─┬─> internal/agent ─┬─> internal/provider ─> internal/wire
           │                 │                   ├─> internal/tool ─> internal/wire
           │                 │                   │        ├─> internal/tool/echo
           │                 │                   │        ├─> internal/tool/fs
           │                 │                   │        └─> internal/tool/shell
           │                 │                   └─> internal/wire
           │                 ├─> internal/compact ─> internal/provider, internal/session, internal/wire
           │                 ├─> internal/session ─> internal/wire
           │                 └─> internal/wire
           ├─> internal/classify ─> internal/permission, internal/provider, internal/tool, internal/wire
           ├─> internal/mcp ─> internal/sse, internal/tool, internal/wire
           ├─> internal/permission ─> internal/tool, internal/wire
           ├─> internal/provider/anthropic ─> internal/provider, internal/sse, internal/wire
           ├─> internal/provider/faux ─> internal/provider, internal/wire
           ├─> internal/provider/openai ─> internal/provider, internal/sse, internal/wire
           ├─> internal/provider/responses ─> internal/provider, internal/sse, internal/wire
           ├─> internal/rpc ─> internal/app, internal/session, internal/wire
           ├─> internal/sandbox ─> (仅标准库)
           ├─> internal/session ─> internal/wire
           ├─> internal/skill ─> (仅标准库)
           ├─> internal/tool ─> internal/wire
           └─> internal/wire

internal/catalog ─> (仅标准库)
internal/classify ─> internal/permission, internal/provider, internal/tool, internal/wire
internal/compact ─> internal/provider, internal/session, internal/wire
internal/mcp ─> internal/sse, internal/tool, internal/wire
internal/permission ─> internal/tool, internal/wire
internal/provider/anthropic ─> internal/provider, internal/sse, internal/wire
internal/provider/conformance ─> internal/provider, internal/wire
internal/provider/openai ─> internal/provider, internal/sse, internal/wire
internal/provider/responses ─> internal/provider, internal/sse, internal/wire
internal/sandbox ─> (仅标准库)
internal/sse ─> (仅标准库)
internal/skill ─> (仅标准库)
internal/theme ─> (仅标准库)
internal/tui ─> charm.land/bubbletea/v2, glamour/v2, lipgloss/v2
```

`internal/wire`、`internal/sse`、`internal/config`、`internal/catalog`、`internal/sandbox`、`internal/skill` 与 `internal/theme` 只依赖标准库；`provider` 不认识 `agent` 和 `tool`（`tests/openai/run.sh`、`tests/anthropic/run.sh`、`tests/google/run.sh` 机械检查）；`tool` 不认识 `provider` 和 `agent`；`session` 只依赖 `wire`，不认识模型、工具或循环；`compact` 只依赖 `provider`、`session` 与 `wire`，不认识 `agent`、`tool` 与任何适配器，也不写会话；`classify` 是 `permission` 与 `provider` 之间唯一的桥：`permission` 声明 `Classifier` 接口却不认识适配器（它仍是只依赖 `tool` 的叶子），实现由组合根造好递进 `Options`，和 `web` 的凭据、`sandbox` 的 `Wrap` 是同一个形状；它不认识 `config`（用哪个模型判是 `cmd/atlas` 的事），也不认识 `agent` 与 `session`，因为分类器只看工具名与参数、看不见对话（`tests/classify/run.sh` 机械检查）；`sse` 只做分帧，不认识厂商 JSON（四家适配器与 `mcp` 共用它，所以它在 `internal/` 根下而不在 `provider` 下面）；`mcp` 只依赖 `sse`、`tool` 与 `wire`——协议与传输在它里面，配置解析在 `config`，连接时机与 `/mcp` 在组合根。`fs` 与 `shell` 互不引用，工具之间的组合只发生在 agent 的批次分发里。`sandbox` 也是叶子，但 `shell` 不引用它：接口（`Wrap(argv, env) → (argv, env)`）由 `tool/shell` 声明、实现由 `cmd/atlas` 注入，所以工具层只需要知道"有个东西能改写 argv"，不需要知道那是 Seatbelt 还是容器（`tests/sandbox/run.sh` 机械检查）。`skill` 也是叶子，而且连 `read` 的 50KB / 2000 行上限都是 `cmd/atlas` 递进来的：它不该知道工具在哪，正如工具不该知道配置在哪。`app` 与 `rpc` 都不认识具体适配器：模型由 `Resolver` 注入，换厂商不动编排与传输代码（`tests/app/run.sh`、`tests/rpc/run.sh` 机械检查）；`cmd/atlas` 是全树唯一引用具体适配器的包，也就是组合根（`tests/cli/run.sh` 机械检查）。`internal/tui` 是全树唯一引用第三方的包，而且只看到 `wire` 与 `theme`：它不认识 `app`、`session` 或任何适配器，终端本身（raw mode、alt-screen、窗口尺寸）由 bubbletea 持有，`cmd/atlas` 只负责把 kernel 接上去（见下面「全屏会话」）。`anthropic`、`google`、`openai` 与 `responses` 是平级的兄弟适配器，互不引用：共用 `provider.Frame`、`provider.FailedStream` 与 `sse`，请求形状与 stop reason 表各留一份。`conformance` 只在测试里被引用：它是跨适配器的一致性用例，方向是适配器的测试认识它，不是它认识适配器。

## 配置

所有设置都在 `~/.atlas/` 下（`ATLAS_HOME` 可覆盖目录），分三个文件：`config.json` 说清楚请求怎么发，`tui.json` 说清楚界面临什么样，`mcp.json` 说要连哪些 MCP 服务器（不写就是一台都不连）。首次运行 `atlas` 会创建 `~/.atlas`（0700）、一份最小可用的 `config.json`（0600）、它引用的 `tui.json`（0600）与一份更完整的 `config.example.json`，然后退出并告诉你该填哪里。`mcp.json` 不建模板：空文件与没有这个文件是同一件事，而教人填一台需要 `npx` 联网的服务器不如让他自己从文档里抄一行。

```json
{
  "defaultModel": "deepseek/deepseek-chat",
  "thinkingLevel": "off",
  "sessionDir": "~/.atlas/sessions",
  "tui": "tui.json",
  "sandbox": {"network": true},
  "providers": {
    "deepseek": {
      "api": "openai-completions",
      "baseUrl": "https://api.deepseek.com/v1",
      "apiKeyEnv": "DEEPSEEK_API_KEY",
      "compat": {"requireReasoningContent": true, "thinkingDialect": "deepseek"},
      "models": [
        {"id": "deepseek-chat", "name": "DeepSeek Chat", "contextWindow": 128000, "maxTokens": 8192},
        {"id": "deepseek-reasoner", "name": "DeepSeek Reasoner", "contextWindow": 128000, "maxTokens": 8192, "reasoning": true, "thinkingLevels": ["low", "high", "max"]}
      ]
    },
    "anthropic": {
      "api": "anthropic-messages",
      "apiKeyEnv": "ANTHROPIC_API_KEY",
      "models": [
        {"id": "claude-sonnet-4-5", "name": "Claude Sonnet 4.5", "contextWindow": 200000, "maxTokens": 8192, "reasoning": true, "input": ["text", "image"]}
      ]
    }
  },
  "web": {"baseUrl": "https://api.deepseek.com", "apiKeyEnv": "DEEPSEEK_API_KEY"}
}
```

`tui` 指向的那份文件就是外观，顶层四个键：`theme`、`colors`、`blocks`、`roles`。

```json
{
  "theme": "auto",
  "colors": {"accent": "#7aa2f7"},
  "blocks": {"composer": {"gapAbove": 1}, "menu": {"width": 80}, "welcome": {"on": true}},
  "roles": {"thinking": {"rows": 8}}
}
```

- `tui` 是个路径，不是子块：相对名相对 `config.json` 所在目录（不是当前目录），也可以写绝对路径或 `~/…`。不写这个键表示不读任何外观文件、全用内置缺省；写了却指不到文件在启动时报错并给出路径——不会静默退回缺省，那样改错了也看不出来。写成空值（`""` 或全是空格）同样报错：写了这个键就是"有一个文件"，空路径是删掉的名字，静默当成"没写"会让"为什么我的配置没生效"变成一个谜。逐键、逐错误的参考见 [docs/tui.md](docs/tui.md)。
- 为什么外观单独一个文件：外观是改得最勤的那部分，而一个写错的色值不该有能力弄坏"哪个端点回话"的那一行。分成两个文件之后，外观的错只报在外观文件里，改错一行就是改错一行。
- 0.4.10 及更早把 `theme` 与 `ui` 直接写在 `config.json` 里。现在不再接受这两个键，遇到会报 `unknown field "theme" (theme, colors, blocks and roles live in the file that "tui" names)`——升级是手工搬一次，之后就只有一个家。
- `api` 是路由键：`openai-completions`、`anthropic-messages`、`openai-responses` 或 `google-generative-ai`。写错在启动时就报错，不会悄悄回退到某个默认端点。
- 模型用 `provider/model` 指定（`-model`、`/model` 命令、`defaultModel` 与会话里记录的模型都是这个"名字对"）。裸 id 只在全配置唯一时可用；`meta-llama/Llama-3-70B` 这类带斜杠的 id 仍按裸 id 解析，因为前缀不是已声明的 provider。
- `/model` 的参数可以带尾随的思考级别：`deepseek/deepseek-reasoner:high` 一次换掉模型与级别（与 pi 的 scoped model 同一写法）。解析先拿整串当模型名，整串解析不了才剥掉 `:level` 再看前缀，所以 `llama3:8b` 这种自带冒号的 id 照旧可用；剥出后缀之后先解析模型、再夹级别：名字不在七档里报"不是思考级别"（拼错不是偏好，不参与夹取），模型没有思考时只有 `off` 能发、其余报错，档位在轴上但不在这一档则夹到最近的一档并打一句 `warning:`——菜单取走的位置本来就是轴上的一格，命令行写一个轴上没有的名字不该比它更严格。
- 凭据：`apiKeyEnv` 指向的环境变量非空时压过 `apiKey`，换 key 不用改文件；两者都没有时 atlas 只警告不退出——本地端点常常不需要 key，而报错信息会指向"缺凭据"而不是真正的问题。
- `baseUrl` 可写在 provider 上，也可写在单个模型上（模型优先）；都可省略，省略时用适配器的默认端点。实际生效的端点会记进 assistant 消息，所以会话文件记的是真正被调用的地址。
- `web` 是搜索与抓取那两个工具自己的块，不是 provider 的字段：`baseUrl` 是**源**（不写就是 `https://api.deepseek.com`），两个工具的路径 `/v1/tools/search` 与 `/v1/tools/fetch` 接在它后面，所以值里带 `/v1` 会被拒绝并说明原因；`apiKeyEnv` / `apiKey` 的规则与 provider 一致（环境变量非空时压过字面值）。为什么不挂在 provider 下：会话由 Anthropic 回话时仍然用这把 key 查资料，挂上去等于换模型时工具悄悄消失。不写这个块就是不要这两个工具；写了但解析不出凭据时只打一句 `warning:` 并不注册它们，不影响别的功能——一个必然失败的工具会教模型把重试当成答案。
- `sandbox` 把 bash 起的子进程关进 macOS 的 Seatbelt：写只允许工作区与 `$TMPDIR`，`.git/hooks` 与已存在的 `.git/config` 被拒，网络按 `"network"` 决定。**不写这个块就是关**（与 0.5.0 一个字节都不变），写空块 `{}` 才是开——默认开着会让一次升级悄悄改变每条命令的行为，而那种变化最容易表现成"某个构建突然坏了"。`"network": false` 是刻意的离线档，缺省放通：`go build` 拉不到模块的失败读起来像工具链坏了，不像一次策略决定。执行器是系统的 `/usr/bin/sandbox-exec`，没有它时启动只警告、每条 bash 命令报错不执行（把"沙箱没套上"降级成"那就不套"等于配置里写着 `sandbox` 而实际拥有整台机器）。它只管 `bash` 的子进程，`write` / `edit` 仍在沙箱外。见 [docs/sandbox.md](docs/sandbox.md)。
- `mcpServers` 不在 `config.json` 里，在 `mcp.json`（全局 `$ATLAS_HOME/mcp.json`，项目 `<cwd>/.atlas/mcp.json`）。为什么单独一份：它里面的 `command` 就是 argv，一份能执行程序的配置和一个写错了只影响"换哪个模型"的配置不该共享同一个解析器与同一次备份；项目级也因此只许写 `enabled`（`auth`、`scopes` 与 `tools` 一并被拒：它们改的是"连到哪、以谁的身份"，不是开或关）。完整形状见下面「MCP」小节，协议取舍见 [docs/mcp.md](docs/mcp.md)。
- 模型元数据默认来自内置目录：`contextWindow` 决定自动压缩阈值，`maxTokens` 是输出上限，`input` 是模态。目录里认得的 id（九条种子：三家 provider 的当前模型）不写这几项就用表里的数，写了就以你为准——一个挂在代理后面的 `gpt-5` 可能确实只有十万窗口。`reasoning` 是唯一不补的：`false` 是"这个端点不思考"的陈述而不是空缺，目录替它打开会让一个只借用 id 的网关开始收思考参数并拒绝请求。`contextWindow` 仍可写 `0` 表示不自动压缩。数据出处、优先级与 clamp 规则见 [docs/catalog.md](docs/catalog.md)。
- `thinkingLevels` 声明这个模型真有哪几档：不写就是六档全有（JSON 里区分不出 `[]` 与缺省），写了就只有列出的这几档，`off` 是开关不是档位，恒可用，写进列表会报错；非 `reasoning` 模型写了也报错。它存在的理由是菜单要说真话：DeepSeek 把 `medium`/`high`/`xhigh` 都翻成 `high`、把 `max`/`ultra` 都翻成 `max`，所以 `["low", "high", "max"]` 才是它真正的三档——`/model` 的左右键只在轴上转。轴是三个来源依次挑出来的：声明了 `thinkingLevels` 用它，没声明但目录认得这个模型用表里的档位，两边都没有才是七档全有。`-thinking` 与 `/model m:level` 落在轴外的档位会夹到最近的一档（等距取更低）并打一句 `warning:`，而不是发一个会被适配器压平的级别——前者静默，后者写在屏幕上。映射永远在适配器里做，声明只负责说清哪几个名字对这家端点是不同的请求。
- `compat` 是端点怪癖开关：`maxTokensField`、`requireReasoningContent`、`disableStreamOptions`、`tolerateMissingFinishReason`、`requireToolResultName`、`requireAssistantAfterToolResult`、`thinkingDialect`，以及 anthropic 的 `version`。为什么只有这些、每个解决什么端点，见 [docs/architecture.md](docs/architecture.md) 第 3 节。
- `compat.thinkingDialect` 只认 `"deepseek"`，作用是把思考参数换成 DeepSeek 文档里的那一套：`openai-completions` 发 `thinking: {"type": "enabled"|"disabled"}` 加 `reasoning_effort`，`anthropic-messages` 发 `thinking: {"type": "enabled"|"disabled"}` 加 `output_config: {"effort": ...}`，两条路都不再发 `budget_tokens`。级别映射两边同一张表：`low` → `low`，`medium`/`high`/`xhigh` → `high`，`max`/`ultra` → `max`；思考在跑时不发 `temperature`（思考模式下它会被忽略，发一个静默丢弃的值等于暗示两者能组合）。`off` 两种格式都发 `thinking: {"type": "disabled"}` 且不带 effort——DeepSeek 的《思考模式》页把开关放在跨 OpenAI 与 Anthropic 两列合并的单元格里，兼容页也把 `thinking` 列成 `Supported`（`budget_tokens` 被忽略），所以这不是猜的取值。省略这个字段时两条适配器一个字节都不变：真实 Anthropic 继续走 `budget_tokens` 预算，OpenAI 继续不发思考参数。模型声明里还要写 `"reasoning": true`，否则 `/model` 菜单那一行不会有左右调级别的轴。
- `theme` 选调色板：`auto`（缺省）按终端背景在 `atlas-dark` 与 `atlas-light` 之间挑，也可以写主题名。主题是 JSON 文件，放在 `~/.atlas/themes/<名字>.json`（全局）或 `<项目>/.atlas/themes/<名字>.json`（项目内同名覆盖全局），文件名就是主题名。文件要么写全十二个语义 token（`text` `dim` `accent` `border` `ok` `warn` `error` `user` `tool` `code` `selection` `background`），要么用 `extends` 继承另一个主题、只覆盖想改的；缺 token、拼错 token、拼错字段一律报错并指名，不做静默回退——半套生效的主题最难发现，因为生效的那半照样画得出来。`background` 写 `"default"` 表示不涂背景、让终端自己透出来。0.4.15 起内置的两套配色自带真底色（`atlas-dark` 是 `#1a1b26`，`atlas-light` 是 `#e1e2e7`），整屏都会被刷成主题的底色；输入区框内的那一层是它朝终端另一头混合 8% 推出来的（方向取主题的 `mode`，不是背景自身的亮度），没有第十三个 token——允许单独写"凸起色"就等于允许写出"凸起比屏幕更暗"这种一眼能看出来、却没有任何理由的错。主题名在这里只查形状（不能是路径），真正的解析在启动时做，因为主题文件不归 `internal/config` 读，名字查不到时由认识主题目录的那一方报错并列出它找到的名字。
- `colors` 是不动主题文件也能改外观的那一层，按语义 token 覆盖当前主题的取值，叠在主题之上而不是替换它：`{"accent": "#ff8800", "dim": "#565f89"}` 只换这两个，别的照旧来自主题，`auto` 事后挑中另一套配色也照样只换这两个。校验走的是主题文件同一张表，token 拼错、色值格式不对在启动时报错并指名，不做静默回退——半套生效的配色最难发现。
- `blocks` 与 `roles` 是几何，按屏幕块命名：一个块是屏幕上看得见的一块地方（它拿走行数、有宽度、四周有空白），一个 role 只决定 transcript 内部那块正文怎么画、不参与屏幕的算术。全部可选，不写就是内置缺省的样子。这些数在启动时读一次，改完重开 `atlas` 生效；越界在读 `tui.json` 时报错并写全路径与范围（`blocks.menu.width: 400 is outside 20–200`），钳制在界面里做——只保证一帧画得出来：

| 键 | 缺省 | 范围 | 作用 |
|---|---|---|---|
| `blocks.transcript.gap` | 1 | 0–4 | 两块对话之间几行空白（"要不要分开"不是设置：那是 prose 与工具树的分界） |
| `blocks.composer.gapAbove` | 1 | 0–8 | transcript 与输入区之间几行空白 |
| `blocks.composer.rows` | 8 | 1–24 | 输入行最多长到几行，超过就从 transcript 借 |
| `blocks.composer.frame` | `true` | | 输入区那一圈圆角框画不画。它占 2 行 4 列，`false` 就是 0.4.14 的样子 |
| `blocks.palette.rows` | 5 | 0–20 | 命令面板最多几行，`0` 关掉面板 |
| `blocks.status.gapAbove` | 0 | 0–4 | 输入区与状态栏之间几行空白，`0` 就是老样子的贴着 |
| `blocks.menu.rows` | 10 | 1–40 | `/model` `/resume` 的菜单最多几行，超出滚动 |
| `blocks.menu.width` | 72 | 20–200 | 菜单框的宽度（含边框），屏幕上放不下时照旧让位 |
| `blocks.approval.width` | 64 | 20–200 | 审批框的宽度（含边框），缺省比菜单窄：问题是一句话，列表有三列 |
| `blocks.welcome.on` | `true` | | 空会话画不画欢迎页，`false` 就是一块空屏加一个提示符 |
| `roles.thinking.rows` | 8 | 0–40 | 思考正文留几行尾巴，`0` 只留标题与时长 |

  为什么这么分组：块是读者看到问题的地方——"输入区太高"和"回答之间没有气口"是两个不同的问题，而一条扁平的十键表要人先自己算出该动哪个。transcript 只有 `gap` 一个键，因为它没有自己的行数：别人剩下的都归它。`thinking` 落在 `roles` 而不是 `blocks`，因为思考块住在 transcript 里面，屏幕算术里没有它自己的一格。0.4.12 的扁平 `layout` 会被拒绝并逐键指路，见 [docs/tui.md](docs/tui.md) 第 9 节。

- 色值与布局的错出现的时间不同：布局的错在读 `tui.json` 时当场报（它能说出块路径与区间），颜色的错留给启动那一刻——它得和主题文件比同一张表，而 `internal/config` 不认识 `internal/theme`，理由见 [docs/architecture.md](docs/architecture.md) 第 3 节。
- 两个文件各自都拒绝未知字段。拼错的 `bseUrl` 会让请求退到厂商默认端点并带着错 key，而报错里一个字都不提配置——所以宁可拒绝解析；`tui.json` 里拼错的 `colour` 同理，静默忽略等于让你以为改了而其实没改。
- 显式 `-config path` 缺文件时不写模板：那是你打的路径，敲错不该留下一个看起来像你自己的配置。

## 命令行

```bash
atlas [flags] [prompt]        # 单次 turn；位置参数就是 prompt
atlas                        # 终端：全屏会话；管道：读 stdin 当 prompt
atlas -rpc                   # pi 的 JSONL 协议，一行一条命令
atlas -demo [prompt]         # 脚本化 provider + echo，不读配置、不联网
atlas -list-models           # 列出配置里的模型与端点
```

| flag | 作用 |
|---|---|
| `-config` | 配置文件路径，默认 `~/.atlas/config.json` |
| `-prompt` | prompt。与位置参数二选一，同时给会报错 |
| `-model` | `provider/model`，或全配置唯一的裸 id |
| `-thinking` | `off` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra`；写错（不在七档里）或给一个不能思考的模型发档位，启动即报错；模型不提供但轴上有的档位夹到最近的一档并打 `warning:` |
| `-continue` | 续跑本目录最近的会话 |
| `-session` | 打开指定会话文件 |
| `-resume <id\|path>` | 续跑指定会话：路径，或 id 前缀（会在本目录的会话里找，歧义时报错而不是猜一个） |
| `-session-dir` | 会话目录，覆盖配置与默认值 |
| `-system-prompt-file` | 换掉内置系统提示词（只管提示词本身，`AGENTS.md` 照样追加） |
| `-no-context-files` | 不读 `AGENTS.md` / `CLAUDE.md`，拿回一字不差的内置提示词 |
| `-no-skills` | 不在系统提示词里列 skill；与 `-no-context-files` 互不影响，两个都关掉才逐字节等于内置提示词 |
| `-cwd` | 工具与会话的工作目录；会话记录的目录已不存在时，用它把会话跑在这里 |
| `-json` | 事件按 JSONL 逐行输出 |
| `-rpc` | 服务 pi 的 RPC 协议 |
| `-demo` | 离线冒烟，忽略 `-config` / `-model` / `-thinking` / `-list-models` |
| `-list-models` | 打印模型表后退出 |
| `-tui` | 强制全屏会话。终端上本来就是默认，所以它只在"明确写出来"与"管道上明确报错"这两个场合有意义 |

全屏会话里只有二十四个命令，其余输入都当一个 prompt：

| 命令 | 作用 |
|---|---|
| `/side [question \| off]` | 开一个侧边对话：继承当前上下文、不落盘；不带参数切进去，带问题顺手问一遍，`off` 关掉 |
| `/btw [question \| off]` | 与 `/side` 同义 |
| `/parent` | 回到主对话，侧边对话留着（`Ctrl+O` 或 `/side` 回去） |
| `/model [<name>[:level]]` | 切模型，写一条 `model_change`（带 `:level` 时连级别一起换，级别变了再写一条 `thinking_level_change`）；不带参数时弹出菜单挑，管道上打印配置里的模型与末行 `current:` |
| `/permission [status\|<mode>]` | 显示或切换权限档位：`status` 报当前档，不带参数时弹出菜单，管道上打印四档；`/permissions` 是同一个命令的别名 |
| `/mcp [<server> \| login <server>]` | 列出连上的 MCP 服务器（名字、服务器自报的版本、几件工具、连上没有）；带服务器名再列那一台的工具，并给出模型真正看到的名字；`login <server>` 走一遍 OAuth 授权（打印 URL，浏览器里点一下，完成报告回到这一行） |
| `/skill [<name> [text]]` | 列出装好的 skill（名字、scope、描述、路径）；带名字则把那份 `SKILL.md` 作为这一轮的请求读进来并开跑，`text` 接在正文之后 |
| `/plugin` | 列出装好的插件：名字、交出了几个 skill、声明了哪几台 MCP 服务器、描述、目录；不带参数，写坏的插件各自一行原因 |
| `/compact [note]` | 手动压缩，可给一句指示 |
| `/continue` | 从断点续跑上一轮：被打断的回答与被插断的工具输出都还在，接着跑而不是重跑；这一轮已经答完就拒绝 |
| `/goal [<objective>\|pause\|resume\|clear]` | 目标模式：不带参数报摘要（目标、状态、轮数、token），带 objective 设目标并开循环，模型连续工作到完成或被打断；`pause` 停循环留目标，`resume` 继续，`clear` 清除。状态随会话持久化，重启后接着干 |
| `/update` | 下载并安装最新版 atlas：打开时已匿名查过一遍（公开仓库 `youverus/atlas`，10 秒超时、失败静默），这里弹菜单确认（upgrade / keep）后按 `SHA256SUMS` 校验并替换二进制，装完重启生效 |
| `/new` | 在同一目录开新会话 |
| `/resume [--all] [<path\|id>]` | 换到另一个会话文件继续；不带参数时弹出菜单挑，管道上打印本目录的会话表；`--all` 打开所有项目的视图，工具与提示词跟着那一行的目录走 |
| `/recover [--all] [<path\|id>]` | 打开一个归档过的会话：不带参数时弹出菜单，管道上打印归档的表；`--all` 同样跨项目 |
| `/archive` | 把当前会话归档：`/resume` 不再列它，文件与消息一个字节不改，然后回到欢迎页（页上是这条命令的收据） |
| `/restore` | 取消归档：会话回到 `/resume` 里，也能正常交互（只能在归档会话里用） |
| `/delete` | 删除当前这个归档会话的文件与标记（**只能删已是归档的**），然后回到欢迎页 |
| `/fork` | 在某个提问之前分叉，或者把整段对话复制到新会话文件并就地切过去（`/fork` 列出可选的点） |
| `/rename <name>` | 给当前会话起名（不带参数时报告现在的名字） |
| `/export [<path>]` | 把当前会话写成文件，`.md` / `.html` / `.jsonl` 由扩展名定格式 |
| `/init` | 让模型读一遍仓库并写下 `AGENTS.md`，写完立刻生效 |
| `/reload` | 显式重读上下文文件与 skill 名单，报告读到了几个文件、几个 skill、多少字节、改没改 |
| `/exit`、Ctrl-D、空行时 Ctrl-C | 退出 |

- 会话默认落在 `~/.atlas/sessions/--<编码后的 cwd>--/`，与 pi 的布局一致，所以两边能读同一批文件。
- 一个 turn 失败不退出会话：常见的失败是请求被拒，改口重说就是。斜杠命令写错则报错而不发给模型。
- 退出手势就这三种，没有 `/quit` 之类的别名：多一个名字就多一行要同步的命令表、帮助文本与 `/help`。
- Ctrl-C 分三层：turn 跑着时连按两下中断这一轮，输入行有字时清空，两者都空着才退出——一次误触不该把整段会话丢掉。在侧视图里前两层不变，第三层换成"退回主对话"：那是往里进了一层，退出来一下就够，输入行里有没有字都一样（草稿跟着视图留着）。侧会话的丢弃仍然只有 `/side off` 一个说法。
- 退出码：0 正常，1 运行期错误（配置错、请求失败、压缩失败），2 命令行解析错误。

### 文件引用（@）

一轮请求里带上文件不用先让模型 `read`：输入行里写一个词首是 `@` 的路径，atlas 在发出去之前把它展开。文本内联成 `<file name="/abs/path">正文</file>`，图片变成同样一个开合标签加一个图片块（MIME 按文件头的魔数定，不看扩展名）。

```bash
atlas "@src/app.go 这个文件在做什么"        # 引号只是给 shell 的，句子照常写
atlas "看 @/tmp/screen.png 里的报错"        # 绝对路径
atlas "看 @~/notes.md"                      # ~ 也行
atlas "看 @\"my notes.md\" 的第一节"        # 路径里有空格就加引号
```

`bob@example.com` 是散文，不是引用——`@` 必须在词首。散文顺手加的尾部标点也不算数：`@main.go,` 先按 `main.go,` 找，找不到才剥掉那一下重试，所以真以点结尾的文件名要写进 `@"…"` 里。

上限是图片 5MB、文本 64KB，超了报错**而不是截断**：塞半份进去，模型会以为拿全了。找不到文件、指向目录、读不出来也一样，这一轮**不发出去**——模型照着你那句话去答一个它没看见的文件，错处只出现在答案里，从答案里看不出来。

全屏会话里 `@` 还是补全的起点：行尾打到 `@` 就列出当前会话目录里的候选（只列一层，目录带尾斜杠，名字里有空格就加引号），`Tab` 就地补全、`Enter` 补了**不**发。补出来的名字走的是 `read` 同一个解析（`fs.ResolvePath`），不会是模型打不开的那一个；只认行尾，因为宿主拿到的是整行文本、不是光标位置。

程序自己拼的正文不解析 `@`：`/init` 那段指令、`/skill` 带上的正文里的 `@` 都是散文。`-rpc` 也不解析——那边的图片走协议里的结构化字段，是客户端面板的事。

### 侧边对话

`/side`（别名 `/btw`）开一段临时的对话：它从主会话当前的最后一个已提交消息取一份快照，装进内存会话，用独立的 agent 跑。**不落盘**——不进 `/sessions`、不进 `/resume`、退了就没了。

侧会话只有一条，所以 `/side` 从不悄悄开第二个：有侧会话时它是"进去"，要一份新快照得先把它关掉。三个动作：

- `/side` 或 `Ctrl+O`：进侧视图。再按 `Ctrl+O` 回到主视图，不重新拍快照。
- `/parent`、侧视图里按一下 `Ctrl+C`：回主视图，侧会话留着，这一行的草稿也留着。键与命令是同一件事的两条路，命令那条给管道和记不住键的人。
- `/side off`：丢掉侧会话并回到主视图。这是唯一丢它的说法，而且它不挂在任何一个键上——一次误按不该丢掉一段对话，而 `Ctrl+C` 在侧视图里已经退得起了。

刚开的侧会话在全屏里是一张自己的首页，第一行写作 `side conversation · N messages inherited`——继承了多少条是这段对话唯一的新事实，所以它没有理由去抄字标和版本。一旦说了话，首页就跟主对话的一样让位给 transcript。页只在"刚开、且这一行没带问题"时看得见，所以另外两种情形仍然打一行 `── side ── N messages inherited · …`：同一行带了问题的 `/side <question>`（答案的第一个字在第一帧之前就到了，页还没画出来就没了），以及纯文本路径（那里没有页可画）。

```
> this function returns three things

╭ 1 call · read ───────────────────────────────────────────────────────╮
│ read  {"path":"internal/app/side.go"} · 96 output lines              │
╰──────────────────────────────────────────────────────────────────────╯

│ It returns the stream, the events, and a function that …

╭──────────────────────────────────────────────────────────────────────╮
│ > _                                                                  │
╰──────────────────────────────────────────────────────────────────────╯
  side | ~/proj/pi-agent-go | #01a16da2 | deepseek-chat | ctx 188k left
```

**两个视图的差别看得见**，因为人只有一份注意力，不能指望他记住提示符里没有的东西：

- 纯文本输出里回答与枝的标记换成空心：`○ ` 与 `  ├ ○ `（主对话是 `● ` 与 `  ├ • `）；全屏会话里两个视图共用同一条竖线，区分靠下面那一段 `side`。
- 状态栏最前面多一段 `side`。
- 两个视图的按键提示不一样，因为能按的键不一样：主视图第二条是 `Ctrl+O switches conversation · Ctrl+C twice exits`，侧视图是 `Ctrl+C or Ctrl+O goes back to the main conversation`。
- 视图切换发生在同一块屏幕上：`transcript`、输入草稿、滚动位置各是一份，切过去看到的是那个视图上次停下的地方。

侧会话里只跑 `/help`、`/side`、`/btw`、`/parent`、`/permission`、`/mcp`、`/skill`、`/plugin` 与 `/exit`，其余命令会被拒绝：`/new`、`/resume` 会把快照的来源挪走，`/model` 会改掉侧会话开张时复制过去的模型。

**只读是宿主把门，不是只写在提示词里。** `read`、`ls`、`find`、`grep` 直接跑；其余调用（write、edit、bash…）每一次都要问一句，答 `y` 才执行，答别的都算拒绝，模型会收到拒绝原因。门开在 `app.Config.BeforeToolCall` 上，问句由界面画成模态框：

```
  ├ ○ Ran  write  {"path":"notes.md","content":"…"} · awaiting approval
  ╭ allow ──────────────────────────────────────────────────────────╮
  │ write  {"path":"notes.md","content":"…"}                         │
  │ y runs it · n skips                                             │
  ╰─────────────────────────────────────────────────────────────────╯
    1 output line
```

- 只吃单键：`y`/`Y` 执行，`n`/`N`、Enter、Esc、Backspace 都是拒绝。别的键既不批准也不拒绝，框还留在那儿等你按——手滑一下不该决定一次写盘。
- 答完框消失，工具结果正好落在它占过的那几行。
- 管道里没有框可以画，仍然打印 `    allow  write  {…}?  [y/N]` 再读一行；`-json` 下不问也不执行，一个字节都不写。

主对话不开这道门，一行都不问。这道门的意义是：临时对话用来问问题、看代码，不该在你走神时改掉工作树；只靠提示词约束的话，一个不听劝的模型就能改。

只剩一处与 MiniMax Code 的 `/btw` 不同，理由是"一次只跑一个 turn"：主任务不会与侧会话并行跑（同一个 `busy` 标志、同一颗 agent），所以侧会话在 turn 之间开、进侧视图时主对话等着。内核变成一次能跑两个 turn 时，这条也该按原设计补回。

### 上下文文件

启动时 atlas 自己读 `AGENTS.md`：不用模型去 `read`，也不用谁手工拼 `-system-prompt-file`。读的范围是 cwd 和它的每一层祖先目录，再加上全局的 `$ATLAS_HOME`（默认 `~/.atlas`）。每个目录里按 `AGENTS.override.md` → `AGENTS.md` → `AGENTS.MD` → `CLAUDE.md` → `CLAUDE.MD` 取第一个有内容的，所以已经有别的 agent 指引的仓库不用为了 atlas 再留一份拷贝。

拼进系统提示词的顺序是**由外向内**：全局文件、最外层祖先、……、cwd。离代码最近的那个文件是最后一句话，因为它最可能就是关于眼前这段代码的。空文件跳过、并且不藏住下一个候选；`stat` 得到却不是普通文件（同名目录、挂载点）也跳过；能 `stat` 到但读不出内容是**报错**，带上路径——静默跳过等于让模型在没有你确认过的指引下作答，最后表现成一个错的答案而不是一个路径。

`-system-prompt-file` 只换掉提示词本身，上下文文件照样追加：那份文件是"怎么当一个 agent"的第二个声音，`AGENTS.md` 说的是这个项目是什么。`-no-context-files` 是唯一退路，用来拿回一字不差的内置提示词（比对模型时有用）。

读发生在**每一轮**：提示词是内核每轮问一次的那个源，不是启动时算一次就固定下来的字符串（0.12.1 起）。所以会话跑着的时候改了 `AGENTS.md`、或者装了一个新 skill，下一轮就生效，不用重开 atlas。读不出来（文件在但读不了）是**报错**，这一轮因此不发出去——模型在没有你确认过的指引下作答，最后表现成一个错的答案而不是一个路径。

`/init` 写完文件后当场重算一次（写完立刻生效，不等下一轮），它打印的那行末尾会写明这一点（`· read into the prompt`），设了 `-no-context-files` 时则写明没读。`/reload` 是同一套读取的显式入口：它回答"我刚改的文件到底进没进"，报几个上下文文件、几个 skill、多少字节、改没改，并逐个列出读到的文件。

与 pi 的差异：atlas 不把系统提示词写进会话文件，所以一份会话文件读回来之后，提示词仍然由**这次启动**的 cwd 与文件决定，而不是由记录的那一份。这样 `git checkout` 换分支、改 `AGENTS.md` 都立刻反映到续跑的会话上；pi 反过来，它把系统消息存进 transcript，恢复时采用的是存下来的那一份（atlas 只在打开 pi 建的会话时采用它，见 `adoptSystemPrompt`）。

### 技能

一个 skill 是一份"这类任务怎么做"的文字：目录里的 `SKILL.md`，frontmatter 写 `name` 与 `description`，正文才是步骤。atlas 只把这两个字段和文件的绝对路径放进系统提示词末尾那段 `<available_skills>`，后面跟一句"匹配到就先读那个 location"；正文一个字符都不进提示词。所以列二十个 skill 的成本是二十条元数据，一个没被用上的 skill 只花这些。

发现只走两层，不递归：根本身，加上根下面一个含有 `SKILL.md` 的目录；根里散放的 `.md` 也算一个。点目录与 `node_modules` 跳过，符号链接跟着走——把一份 skill 放一处、三处引用，靠的就是链接。skill 自己的 `references/` 与 `scripts/` 不再下探：把那里当第二个根，会让一个 skill 变成好几个。

五类根，按序读（第三类每装一个插件多一行）：

| 根 | 谁装的 |
|---|---|
| `$ATLAS_HOME/skills`（默认 `~/.atlas/skills`） | 你自己 |
| `~/.agents/skills` | 你自己，共享拼写，同族的其他 agent 也读这里 |
| `<插件目录>/skills` | 插件作者（见下一节），一个插件一根 |
| `<cwd>/.atlas/skills` | 随仓库来的 |
| `<cwd>/.agents/skills` | 随仓库来的，共享拼写 |

这个顺序就是同名冲突的规则：先到先得，所以项目里的 `release` 顶不掉你装的那个，只会让自己的那条被忽略并打一行 warning；插件根排在项目根之前同一个道理——插件是你装的，仓库是别人给你的，一个 checkout 不该顶掉你装的插件里的 `release`。`AGENTS.md` 那种向祖先上溯在这里刻意没有：上下文文件说的是"这棵树是什么"，越往上越外；skill 说的是"这类活怎么干"，跟着 `cd` 换一份菜单，用户没法从任何地方重建出"为什么这里三个、那里五个"。

`name` 与 `description` 都必填。有的实现允许省略描述、拿正文第一行顶上，atlas 不：描述是模型唯一的挑选依据，缺了就永远挑不到，与其列一条挑不到的，不如跳过并说一句。`name` 只许小写字母、数字与 `-`，最长 64 字符，不许首尾是 `-`、也不许连续两个；`description` 超过 1024 字符就截断并警告。目录名与 `name` 不一致时名字赢并警告：规范要求两者一致，但重命名目录不该等于这个 skill 消失。

frontmatter 只认一个 YAML 子集：单行标量、`|` 与 `>` 两种块标量、`#` 注释；锚点、别名、流式集合直接报错，并且报错带行号。不认识的键整块跳过（连缩进一起），所以别人家写的 `metadata:` 不会把文件变成地雷；`allowed-tools` 与 `disallowed-tools` 例外，读它们只是为了说"没读"——这里的审批是会话级工具名名单，文件里的一行声明预批准不了任何东西。

读坏了的 skill 只出声，不拦启动：`skill: <路径>: <原因>` 一行一条，作者以为装好了的东西不该静默消失。而整个根不存在则一句话都没有——新装的机器一个根都没有，为一堆没人要求创建的目录报警会把真正该看的警告淹掉。正文超过 `read` 的 50KB / 2000 行时列表里只警告：那条上限意味着"模型自己读会拿到截断的正文"，而把整个 skill 丢掉比留下它更糟。`/skill` 那条路不一样，它拒绝（见下一段）。

正文由模型用现成的 `read` 取，这一条决定了整个功能：skill 不授予任何能力，它是用户自己也能打出来的一段字，所以审批门与沙箱照旧生效。提示词每轮重算（0.12.1 起），所以会话跑着时装完 skill，下一轮就看得见，不用重开 atlas；`/reload` 能当场报出读到了几个 skill、几个文件。模型挑漏时用 `/skill <name> [text]` 直接指定：它把那份 `SKILL.md` 连同一个 `<skill name="..." location="...">` 外壳作为这一轮的请求读进来，`location` 是正文里相对路径的解析基准，`text` 接在正文之后当同一次请求。那条路超限就拒绝、不截断——模型以为拿到了全部指令、实际只有半份，是这里唯一不能出的错。裸 `/skill` 是同一张表的只读版（名字、scope、描述、路径），一个都没有时打出每一个根。

### 插件

插件是**一个目录，一个 `plugin.json`，声明两样东西**：一组 skill，几台 MCP 服务器。它不是程序，没有入口、没有生命周期、没有版本号，装它就是把目录放进去，卸它就是删掉目录。

```
$ATLAS_HOME/plugins/review/
  plugin.json
  skills/release/SKILL.md
```

```json
{
  "description": "Review helpers.",
  "skills": "skills",
  "mcpServers": {"lint": {"command": "lint-server", "args": ["--stdio"]}}
}
```

三个键都能省，但不能全省：`{"description": "..."}` 会被拒（"contributes nothing"）——一个什么都不加的插件更可能是写了一半。`mcpServers` 与 `mcp.json` 里那个对象**同一个形状**，`docs/mcp.md` 第 7 节的配置形状整份适用，校验也是同一段代码。未知键报错，与 `config.json`、`tui.json`、`mcp.json` 一致。

只有这一个根，**没有项目级插件**：一个 checkout 里能声明插件等于把任意 argv 交给仓库，与项目级 `mcp.json` 只能开关服务器是同一条理由。`skills` 是插件目录内的相对路径，词法校验 —— 绝对路径、`.` 与 `..` 开头的都拒，符号链接不解析也不拒绝。名字不合规的目录跳过，没有 `plugin.json` 的目录不算插件也不出声。

两类贡献各走各的路，都不新增机制：

- **skill**：那个目录变成发现顺序里的第三类根（见上一节的表），所以它的 skill 出现在 `<available_skills>` 里，也能被 `/skill <name>` 直接读；`/skill` 的 scope 列写 `plugin`，路径列说清是哪个插件带来的。
- **MCP 服务器**：折进 `mcp.json` 那张表，顺序是 `mcp.json` → 插件（按名）→ 项目文件，**撞名先到者赢**并打一行 note。远端工具照旧注册成本地工具，所以它们与内建工具同一张表、同一套审批门、同一套截断。

一个坏插件的代价分两种粒度：声明本身坏了（JSON 解不开、键拼错、路径逃出目录）整个插件不加载，说一行 `plugin: <路径>: <原因>`；声明的 `skills` 目录不在，只丢那一半，服务器仍然有效。**无论哪种，一台机器上其余的插件照旧加载**——插件是一个目录之一，不是一份会被整体拒绝的文件。这些原因都进启动时的 warning，不必等 `/plugin`。

`/plugin` 只读、不带参数：一行一个插件，五列——名字、交出了几个 skill、声明了哪几台 MCP 服务器、描述、目录。SKILLS 列写的是**本次会话真拿到的个数**（同一个目录里被更靠前的根遮住的那个不算），不是目录里躺了几个文件。

刻意不做的：市场、远程安装、启用/禁用开关、版本与依赖解析、签名与信任链、上架评分，以及"插件能加命令、加工具、加钩子"。最后一条与市场是同一条理由 —— atlas 的权限模型只认工具名与命令前缀，插件能声明的东西因此只能是**已有的两类**：skill 是文字，MCP 工具走服务器自己那一套。（完整格式与十八条验收见 [docs/plugins.md](docs/plugins.md)。）

### 会话命令

`/resume`、`/fork`、`/rename`、`/export`、`/init` 都在同一份命令表里，行为如下。

- `/resume` 换到另一个会话文件继续，并把会话记下的旋钮一起带回来：模型、思考级别、系统提示词、名字，与启动时 `-continue` 的恢复是同一条路径。恢复完成后屏幕会把这段对话重放一遍：`history: N messages` 起头，用户行以 `> ` 开头（多行粘贴按缩进续行），回答以 `│ ` 起头且每行都有（全屏会话里由 transcript 的块画，折行也在块内管）、工具调用按 live 的枝形一行一条（调用与它自己的结果合成一行，产出条数照旧；耗时不在会话文件里，所以重放不写），只有失败的结果才带缩进正文，孤儿结果（调用不在对话里）单独成行，调用没等到结果的中断轮写 `· no result`，模型把这一轮跑成错误的则补一行 `error:`。`-resume` / `-continue` 启动时目前**不**重放：全屏会话只显示欢迎页，历史要再用 `/resume` 换一次才看得到（这条记在 `docs/architecture.md` 第 9 节的缺口里）。空会话没有可重放的东西，因此不打这一段。裸 `/resume` 在全屏会话里弹出一个菜单：`↑`/`↓` 环绕、`PgUp`/`PgDn` 跳一屏、`Home`/`End` 到两端、`Enter` 取那一行、`Esc` 走开（什么都不改）。每行是会话名（没名字时 `session <id前缀>`），右列是「几分钟前 · 几条消息 · 首句」，空会话不写预览那一截（`0 messages` 已经把话说完了）。菜单每次打开都重读本目录，所以 `/new` 与 `/fork` 加进来的文件下次就在；它开在当前会话那一行上，所以「我本来在哪」不用去状态栏对。管道与 `-prompt` 上没有键可按，那里退回原来那张表格（id 前缀、消息数、最近活动、首句），两条路径读的是同一份列表，也都认 id 前缀（状态栏显示的那段）与路径。切换是全有或全无：装载、cwd 校验、重投影全部成功才动手，失败时当前会话原样不动。turn 进行中拒绝（先 Ctrl-C 再换）；steering/follow-up 队列与侧会话跟旧对话一起走，排队的话不会打进新会话。工具是按启动时的 cwd 建的，所以别的项目的会话会被拒——去那个目录再开 atlas。id 前缀匹配多个会话时报错列出能区分的形式，不猜。`/new` 与切换都不补写文件：会话文件从第一条 user 或 assistant 消息开始，一句话没说的会话切走就没了（它本来也没有可回去的内容），说过话的文件一个字节不改、随时还能切回来；`-continue` 仍然跳过没有对话条目的占位文件（旧版本留下的，或写到 header 就断掉的那次写），不会续到空壳上。


  ```
  resumed: session 01a172f2
  file: ~/.atlas/sessions/--tmp-proj--/2026-10-25T12-04-18-052Z_….jsonl

  history: 4 messages

  > remember the milk

  ├ • Ran  echo  {"text":"remember the milk"} · 1 output line

  ● demo complete

  ```

- `/fork` 从 0.12.15 起回答的是"在哪儿分叉"：不带参数时列出可选的点（有界面就弹菜单，管道与 `-prompt` 上打一张编号表），第 0 行是整段对话，之后每条你自己写的话各占一行；`/fork <编号>` 直接取那一行。在某一句话之前分叉时，那句话会回填进输入行——分叉到它跟前要的就是换个说法，让人重新打一遍等于把这个命令省下的那件事还回去。它把当前分支复制到新会话文件，然后就地切过去：旧文件一个字节不变，两份都能 `-continue`；新文件 header 的 `parentSession` 记旧文件路径，之后每条消息只写新文件。名字跟着 fork 走（`session_info` 在分支上）。in-memory 会话 fork 后仍然是 in-memory：新 id、同样的 entries、不落盘，命令行会注明 `(in memory)`。它不中断对话：队列、side 会话、历史都还在，只是下一个 turn 写到另一个文件里。一句话都没说过的空会话会被拒绝（`nothing to fork`）：那一步只会换掉你正在看的 id，不会分叉出任何东西。
- `/rename` 写一条 `session_info`，换行折成空格（会话名是一行，状态栏也只有一行），打印的是**存下来**的名字。不带参数只报告名字（没名字时 `name: (unnamed)`），不清空：查看和删除不该共用一个手势。
- `/export` 默认写 `<cwd>/atlas-session-<id8>.md`，前缀与状态栏的会话段一致，所以同一会话导出两次是覆盖同一个文件，而不是堆一串带时间戳的副本。给了路径就写那里（`~` 会展开，相对路径相对会话 cwd），**不自动建父目录**：打错的路径应该像打错的路径。格式由目标的扩展名决定，因为三种格式答三个不同的问题。`.md` / `.markdown` 是要读的文档：标题（有名字用名字，否则 `atlas session <id8>`）、五行元数据（session / cwd / model / 消息数 / 导出时间）、`---`，然后一条消息一节 `## User`、`## Assistant`、`## Tool result: <name>`（错误结果标 `(error)`）；system 消息不导出（每个会话都一样，而且是文件里最长的部分）；thinking 折进 `<details>`，留着但不挡路；工具调用写成 `**Tool call: <name>**` 加缩进过的 JSON；工具结果用 `text` 块，空输出写 `(no output)`；参数不是合法 JSON 时原样写出，导出不该替模型圆谎；栅栏长度按正文里最长的反引号串加一来定，所以工具输出里嵌着代码块也不会把文档撕开。`.html` / `.htm` 是同一份内容的另一种读法，单文件自包含——样式内联、没有脚本、打开时不取任何东西（因此离线可读，也不必信任网络），浅色之外还有 `prefers-color-scheme` 的深色；回答里的表格、围栏代码块、标题、列表与行内样式都真的画成了 `<table>`、`<pre><code>`、`<h1>`–`<h6>`、`<ul>`、`<strong>`，不是把 Markdown 原文塞进 `<pre>`；写的语言名进 `class="language-…"` 之前只留字母数字与 `+#._-`，链接只放行 `http` / `https` / `mailto` 与相对路径（`javascript:` 之类的写成文本），因为这份文件会被浏览器打开。`.jsonl` / `.ndjson` 不是文档而是一个会话文件：header 加当前分支的条目，`parentId` 重新串成一条链，所以 `atlas -resume <path>` 能直接把它打开——导出与存储在 `internal/session` 里共用同一个编码器。图片三种格式都只写一行 `[image: <mime>]`，不内嵌：截图是兆字节级的，导出是要读的文档而不是档案。想不给路径还要 HTML 或 JSONL 时用裸 `--md` / `--html` / `--jsonl`（`--markdown` / `--json` 也认），那是「默认文件名加这个格式」的简写；其余写法在开文件之前就拒绝，并说出这三种名字。
- `/init` 是跑一轮模型，不是套模板：AGENTS.md 写的是这个仓库，只有读一遍才知道里面是什么，模板只会产出一份对任何项目都成立、因此没有用处的文件。提示词要求先读构建与测试命令、文档和几个代表性源文件，再用 write 工具写根目录；已有文件则先读再改；不许写没在仓库里见过的命令。跑完做一次 `os.Stat`：模型说"写好了"却没落盘时，报 `AGENTS.md was not written` 而不是打一行成功并让用户去开会话文件；文件在，就重算一次系统提示词，下一轮直接生效。

会话属于哪个项目由 header 里的 cwd 决定，目录名只是"桶"：编码 `--<路径，其中 / \ : 都换成 -->--` 是多对一的，`/x/a-b` 与 `/x/a/b` 会落进同一个目录，所以 `-continue`、`/resume` 的 id 前缀与菜单都按 header 的 cwd 过滤，用的是同一个 `samePath` 谓词（尾斜杠不影响判断）。目录编码一个字节都没改，pi 仍然读同一批文件。

`/resume --all` 与 `/recover --all` 是跨项目的那两个入口：配了 `-session-dir`（flag 或配置）时它们只列那个目录但不按 cwd 过滤——那是一处仓库，不是一棵项目树，所以不声称跨项目；否则遍历 `~/.atlas/sessions/*` 每个桶。表格多一列 cwd（按 `shortenPath` 缩成末两级），footer 写的是 `resume one with /resume --all <id prefix>; its tools run in the directory on its row`：看得见的行必须能被命名，所以 `--all` 除了开菜单还能带一个 id 前缀。切过去是真的切过去——`dir:` 那一行只在目录变了时出现，告诉你工具现在在哪：`fs` 的边界、bash 的沙箱可写根、`AGENTS.md` 与上下文文件都换成那个项目的。唯一留下的拒绝是"记录的那个目录已经不在了"，因为删掉的项目托管不了工具。

记录过的目录被删掉时在启动阶段就失败：`-resume` / `-session` 报出记录的目录、当前目录和两条出路（`cd` 回去，或加 `-cwd <dir>` 就地跑），此时任何工具都还没执行过，所以失败是干净的；给了 `-cwd` 就用它重开会话，并在 stderr 打一行 `note:`。`/resume` 换会话没有 `-cwd` 可用（工具按会话的目录重建，不能替它编一个不存在的目录），因此直接拒绝。`-continue` 不在这一列：它本来就用当前目录覆盖 header 的 cwd，项目目录搬过家也能继续。

### 归档会话

`/archive` 把当前会话收起来：之后 `/resume`、`-continue` 和选择器都不再列它，但**文件和消息一个字节不改**——它没有被删掉，只是被放到另一张表里，`/recover` 那张。两者是同一套选择器的两半（同样的行、同样的菜单、同样的 `age · N 条消息 · 首句`），区别只有读哪一半目录，所以 `/recover` 不带参数时弹的是菜单，管道上打的是归档表，id 前缀也各自在本表里解析——同一个前缀在 `/resume` 下查不到，在 `/recover` 下查得到。

归档标记是同目录下的 `<session>.jsonl.archived`，一个空文件，存在即是状态。不能写进会话文件是因为 v3 JSONL 与 pi 字节级兼容（pi 不认识的字段写进去就不再是"同一个文件"）；不能让 `List` 自己记一份是因为 `-continue`、`/resume`、`/recover` 与 `-session` 必须对"这个会话算不算数"给出同一个答案。归档也不改文件的 mtime，所以 `/restore` 之后它在按活动排序里的位置还在原处。一句话没说的会话没有文件，因此拒绝归档（`ErrNothingToArchive`）——藏一个不存在的东西不是归档。

打开一个归档会话（`/recover` 的菜单或参数、`-resume <path>`、`-session <path>`）之后，它变成只读：`/resume` 看到它时屏幕上会先打一行 `archived: read-only here; /restore puts it back in /resume, /delete removes the file`，之后除了 `/help` 那一句列出的命令什么都跑不了——`/help` 说一句这里还剩哪几条，`/restore` 把它放回 `/resume` 并恢复交互，`/delete` 删掉文件与标记，`/permission` 只读写那一个所有会话共用的档位（不碰文件；这里跑不了 turn，所以它说的是"接下来的会话会怎样"，`status` 那一半是不 `/restore` 也能问档位的方式），`/mcp` 与 `/plugin` 只是把服务器与插件列一遍（读连接与插件目录，都不写会话），`/skill` 的列举一半是读而装载一半自己拒绝，`/reload` 重读提示词文件而记录不动，`/exit` 走人。发 prompt 也算写操作，同样被拒。这道锁有两层：命令层（`archivedCommands`）决定"哪些命令在这跑"，`app` 层（`guardArchived`）保证"没有写操作能绕过命令层落到归档文件上"——`rpc`、测试、下一版的前端都不经过命令表，而记录被悄悄改掉是这一层唯一不能靠界面保证的事。

`/archive` 与 `/delete` 成功之后都落在一个**空**会话上（`/archive` 是为了接着做别的事，`/delete` 是因为文件已经没了），所以屏幕回到欢迎页，收据变成页上那一行——归档完成的信号就是"回到首页"，而不是某个空会话里的一行字（见上面「全屏会话」）。一个例外：`/resume` / `/recover` / `/fork` 换到的是**有内容**的会话，那里 transcript 还在，报告照旧打在 transcript 里，历史也照旧重放。`/delete` **只对归档会话有效**：它是 atlas 里唯一销毁 transcript 的命令，而归档正是用户特意要求的"第二眼"。跨项目的归档列表还没有：`/recover --all` 会被明确拒绝，`-resume <id 前缀>` 也找不到归档会话，要按路径打开（这条记在 `docs/architecture.md` 第 9 节的缺口里）。

### 全屏会话

终端上 `atlas` 直接进全屏（alt-screen）：退出时终端原样还回去，终端自己的历史缓冲区里不留这一屏。`-tui` 只是把这条默认写出来，管道上给它会直接报错。

还没有对话时屏幕上是一张欢迎页——字标、版本、启动目录、模型与权限，加两条最常用的手势：

```
                        ███  ███  █    ███  ███
                        █ █   █   █    █ █  █
                        ███   █   █    ███  ███
                        █ █   █   █    █ █    █
                        █ █   █   ███  █ █  ███

                   atlas 0.12.3 · ~/proj/pi-agent-go
             deepseek/deepseek-chat · think high · fs+bash

                 Type a message, or / for the commands
           Ctrl+O switches conversation · Ctrl+C twice exits
```

它是"还没有内容"的占位，不是一块会滚走的东西：第一行一提交就换成 transcript，`/resume` 重放出来的历史也一样。终端矮到放不下时收成一行 `atlas 0.12.3`；宽度不够时先丢目录，再丢思考档位与权限——版本和模型比路径更能说明"这是哪个会话"。

打开 atlas **不新建会话**：屏幕是这张页，页脚里没有会话段，磁盘上没有文件；第一条消息发出去，会话 id（与文件、与状态栏那一段）才同时出现。规则是「没有内容就没有会话」——一个一句话没说的空会话不是会话，是这张页，而页脚里挂一个 `#01a17b1f` 会让它看起来像已经有一个了。`/archive` `/delete` `/new` 把当前会话换成一模一样的空会话，所以它们落地之后也是这张页。

页上还能多出一行，是那条命令的收据：

```
        atlas 0.12.3 · /tmp/sessbug
             fake/one · think high · fs+bash
   archived: session 01a17b2d · /recover opens it again

          Type a message, or / for the commands
      Ctrl+O switches conversation · Ctrl+C twice exits
```

这一行写在页上而不是写进 transcript，因为 transcript 正是这张页让位给它的东西：一条 notice 会把页顶掉，读者最后看到的是"归档了"加上一个还不存在的会话 id。收据只留命令自己的头一句（`archived: …` / `deleted: …`），后面那几行说的文件与"新会话"在这里都是废话——页本身就是"新会话，还没说话"；`/archive` 额外带上 `/recover`，因为那是唯一回去的路。`/new` 一句都不说：它没留下任何需要报的东西。任何一行提交之后这行收据就作废（那时页也已经不在了）。

侧对话也有自己的一张，而且只有"刚开、还一句话没说"的时候才有：一开出来就有字的那张没有欢迎页，因为它已经没什么可欢迎的。这一张不画字标也不报版本（这两样说的是"这是哪个程序"，而侧对话要说的恰恰不是这个），第一行换成一行说明，其余照旧，两条手势换成侧视图能用的那两条：

```
        side conversation · 12 messages inherited
              fake/one · think high · fs+bash

          Type a message, or / for the commands
   Ctrl+C or Ctrl+O goes back to the main conversation
```

继承了多少条只在打开的那一刻才知道，所以这张页是每帧问宿主要一次（`Config.Welcome` 收的是视图号），而不是启动时一次性填好。

有一屏对话之后：

```
> explain the retry loop

╭ Thought for 6.4s ────────────────────────────────────────────────────╮
│ the retry wrapper is the only place that sleeps                      │
╰──────────────────────────────────────────────────────────────────────╯

╭ 1 call · read ───────────────────────────────────────────────────────╮
│ read  {"path":"internal/agent/loop.go"} · 240 output lines           │
╰──────────────────────────────────────────────────────────────────────╯

│ The loop retries on transport errors …

╭──────────────────────────────────────────────────────────────────────╮
│ > _                                                                  │
╰──────────────────────────────────────────────────────────────────────╯
  …/pi-agent-go/go | #01a16e7d | deepseek-chat | ctx 8.2k left
```

一屏三段：上面是 transcript（内容贴底，对话刚开始时不会浮在屏幕顶上），中间是输入行，最下面是状态栏。

五种块各有各的形状，靠字形而不是亮度区分：自己打的那句是 `> ` 开头、整行（marker 和正文）走 `user` 这个颜色，前后各空一行——它是这段对话里唯一不是模型写的东西，也是回头翻找时唯一要找的那一样；模型的回答每行都以一条 `│ ` 起头（贯穿整块，滚到哪儿都还认得出来）；一段思考是一个盒子，标题是它的时长，正文留尾巴（`roles.thinking.rows`，缺省八行，被截时首行以 `… ` 起头）；一段连续的工具调用也是一个盒子，标题是 `2 calls · read, grep` 这种计数加名字（最多三个，多的写 `+3` 之后的数），里面一块一行：名字、参数、结果，失败正文再缩进两格；提示与错误仍是一行一块。留白只加在"换了一种东西"的地方：盒子与盒子之间、以及用户行的两侧；段内不留——框已经把两段分开了。窄屏先缩标题（`2 calls · read, grep` → `2 calls · read` → `2 calls`），缩到底还放不下才退回一行一块：盒子是结构、标题是描述，窄屏该丢的是描述。

输入行里打 `/` 会弹出命令面板，最多五行（`tui.json` 的 `blocks.palette.rows`），左边是命令与它的参数，右边一句话说明它做什么：

```
▸ /help                  list these commands
  /side [question|off]   open a temporary conversation beside this one
  /btw [question|off]    the same as /side
  /parent                return to the main conversation, keeping the side one
  /model [<name>[:level]]  switch model, or pick one from the list
```

`↑` `↓` 选行，`Tab` 把名字填进输入行，`Enter` 边填边提交（所以 `/mo` 加回车跑的是 `/model`，而不是一条不存在的命令）。匹配的是第一个词，参数跟着一起打的时候说明仍然挂着：`/model deep` 正是要决定参数写什么的那一刻。侧视图里这份列表只剩六条在那儿能跑的命令，与 `/help` 末尾那行说的一致——面板不该是界面上唯一一处对"什么能跑"撒谎的地方。宽度放不下两列时只留命令名。

| 按键 | 作用 |
|---|---|
| 可打印字符、粘贴 | 插到光标处。跑 turn 时照样能打，整行等着提交 |
| `←` `→`、Ctrl-A/E | 移动光标；菜单开着时转那一行的思考档位（`←` 更少、`→` 更多，到两端就停，不环绕） |
| `Home` `End` | 行首 / 行尾 |
| Ctrl-U、Ctrl-W | 删掉光标前的一个词 |
| Backspace、Delete | 删前一个、后一个字符 |
| Enter | 提交；有命令面板时先把选中的命令填进去；行尾是一条 `@` 候选时先把它填进去、**不**提交（再按一次才发）；菜单开着时取那一行。turn 进行中不响应（它不排队，见下） |
| `Tab` | 把那行选中的命令填进输入行；行尾是 `@` 候选时就地补全（替换那个还没打完的路径，句子其余部分不动） |
| `↑` `↓` | 菜单开着时换行（环绕）；命令面板开着且不止一条时选行；否则历史（同一视图内）。`Shift`/`Alt` + `↑`/`↓` 永远是滚一行 |
| PgUp / PgDn、滚轮 | 上下翻页；菜单开着时跳一屏（`Home` / `End` 到两端） |
| 拖过对话（鼠标） | 选中并复制到剪贴板（OSC 52），状态栏末尾报一句 `copied N chars`。选区按单元格算、跨行也能选，滚轮不影响已经选好的范围；`Esc` 清掉。按住 Shift 仍是终端自己的选择 |
| 单击一段的标题行（鼠标） | 把这一段收起来（思考、或一段连续的调用），收起来是一行 `▸ <标题>`；再点一下展开。点盒子的正文是选字，不是折叠 |
| Ctrl-G | 把每一段思考与调用都收起来，再按一次全部展开。框关掉（`blocks.transcript.frames: false`）时这个键什么都不做 |
| Ctrl-Home / Ctrl-End | 回到最上面 / 跳回底部 |
| Ctrl-O | 在主对话与侧对话之间切；没有侧会话时提示 `/side` |
| Ctrl-C | 跑着时连按两下中断这一轮（第一下只是提示，中间按别的键就撤掉）；菜单开着时只关掉菜单（不参与退出武装）；侧视图里一下退回主对话，侧会话和这行的草稿都留着；主对话里空着时第一下清空并提示 `ctrl+c again to exit`，第二下退出 |
| Esc | 跑着时连按两下中断这一轮（与 Ctrl-C 共用一个手势，两种键混着按也算两下）；菜单或者审批框开着时走开（不改变任何东西）：那个键被模态框用掉了；其余时候先清掉拖选中的文字，没有选择就什么都不做 |
| Ctrl-D | 空行时退出 |

- 中断是两次按键而不是信号：raw mode 由 bubbletea 持有，Ctrl-C 不再是 `SIGINT`，`Model` 自己 `cancel()` 掉这一轮。**为什么两下**：一轮跑出去的东西收不回来——写了一半的文件、已经发出去的命令、正在读的仓库——而 Esc 与 Ctrl-C 都是手滑能碰到的键，一下就把答案丢掉太便宜了。第一下只武装，输入行写 `esc again to interrupt`；中间按下任何别的键都撤掉，与退出那套是同一个规则。两种键共用一次武装（先按 Esc 再按 Ctrl-C 也算），因为数的是"读者停了两次"，不是某一个键两次；模态框吃掉的那一下也算"别的键"——审批框里的 Esc 是"不准"，同一个键不该在一屏上有两种读法，更不该顺手把那一轮停掉。
- 中断之后要还的东西有四样，缺一样就会露出痕迹来：ctx 一取消，下层的流、命令的进程组、等着人回答的门禁都自己收摊；transcript 里正在跑的工具行改写 `· interrupted`（内核给的是 `Operation aborted` 之类的话，照抄就是把人自己按的键说成故障）；供应商把取消写进 `ErrorMessage`（`openai: request aborted`）也不算错误，不涂红；排队里的 steering / follow-up 交回输入行（`Config.Interrupt` 由宿主清空两支队列并返回文本，草稿接在后面），既不能塞进下一轮——那是对一段刚被放弃的对话说话——也不能丢，它只存在这儿。剩下的就是把这一次说清楚：一行 `interrupted`。
- 中断之后还能接着走，`/continue` 就是那条路（0.12.16）。断点不是另记的标记，是对话尾部那条消息的形状：被打断的回答留着已经说出去的那段正文（thinking 与没配上结果的 tool call 会丢掉——前者没有尾部签名回放不了，后者没有配对的结果会让端点拒绝整个请求），被插断的工具结果留着 `Operation aborted` 那一行加它已经产出的输出，两者都打上 `interrupted`。`/continue` 读这个形状决定怎么续：尾部是助手消息时补一条 `Continue from where you were interrupted.`（以助手消息结尾的请求是 prefill，四家实现里一家有文档、另一家直接拒），是工具结果或用户自己说的话就直接跑。这一轮已经答完（`stop` 收尾）则拒绝，报的是"没什么可续的"而不是悄悄重跑。能这么读是因为断点就在 transcript 里：重启之后用 `-continue` 打开同一个会话，`/continue` 一样接得上。
- 退出要两下：一下就把人踢出会话的键，离"我要重写这句话"太近了。中间按下任何别的键都会撤掉这次武装——它不是"一秒内再按一次"的计时器，而是一个清楚的状态，提示就写在输入行里。两下只在主对话里才是这意思：侧视图往里进了一层，所以那个键在那儿是"退出来"，一下就够，而且什么都不丢——宿主留着侧会话，每个视图各有自己的输入草稿，`Ctrl+O` 就能回去。丢掉侧会话只有 `/side off` 一条路，正因为误按一下不可恢复，它不挂在任何一个键上。
- 两个视图是两份状态：各自一段 transcript、一份输入草稿、一个滚动位置。`Ctrl+O` 只把"请切换"交给宿主（`cmd/atlas` 的 `sideState`），界面不自己决定落到哪儿；`Ctrl+C` 的"后退一级"同理，界面问 `Config.Back` 要"有没有上一级、上一级是哪个视图、路上写哪句"，自己不知道"侧"是什么。
- 提交流中的输入不排队：`Enter` 在 turn 进行时被忽略，而不是攒起来在下一轮跑掉——攒着的行会让人以为已经问过了。
- 事件成批到达（一个回答的几十个增量、一次工具结果的十几行）之后整屏重画一次。理由写在 `docs/architecture.md` 第 3 节 `tui` 的第 3 条：bubbletea v2 底下的增量绘制在这种批次上算不对行位移，宁可从零画一帧。
- `AltScreen` 换来的是"主对话不会被侧对话挤走"，代价是终端历史里不留这次会话——要存下来用 `/export`。
- 命令面板与欢迎页都不认识命令表：前者由 `cmd/atlas` 通过 `tui.Config.Suggest` 逐次给出候选，后者由 `Config.Welcome` 按视图号填进版本、目录、模型与权限（侧视图那一张换成"继承了多少条"和一个标题，字标与版本都不出现）。界面上没有一个写死的命令名，也没有第二份"这个会话是什么"的说法。
- 收据与 transcript 不走同一条路：命令落地在一张空会话上时，`cmd/atlas` 交给界面的是 `Sink.Flash`（页上那一行），而不是 `Sink.Note`（transcript 的一块）。这两者之所以必须分开，是因为 transcript 非空就没有欢迎页——用 notice 报"回到了首页"会把首页本身顶掉。收据只活在主视图的页上，提交下一行就清掉。
- 菜单（`/model`、`/resume` 不带参数时）是一个模态框，与审批框共用同一个位置：命令的 goroutine 阻塞在 `tui.Sink.Pick` 上等一个答案，界面只负责画和收键。它同样不认识"模型"与"会话"——`tui.Choice` 的 `Value` 对界面不透明，`Current` 只是"开在哪一行"，候选与当前值都由宿主填。菜单开着时按键先归菜单，输入行既不显示光标也不显示 `esc twice to interrupt` 那类提示——同一屏上同一个键不该有两种读法（审批框同理：那儿的 Esc 是"不准"，不是"停"）。横轴也是宿主给值、界面给位置：`Choice.Steps` / `Choice.Step` 属于**那一行**（几个模型各有各的档位集合，不会思考的模型这一列根本不存在），界面只知道"这一行有个刻度"，不知道刻度叫思考档位。第二个菜单、或者一个菜单碰上一个还在问的审批框，一律按"空答案"拒绝：一次只有一个模态。

审批是盖在对话上的模态框：侧会话里白名单之外的调用会出现一个 `allow` 框，三个答案——`y`/`Y` 跑这一次、`a`/`A` 这一个工具在本次会话里不再问、`n`/`N`、Enter、Esc、Backspace 跳过，其余键什么都不做（问题继续挂着）。问不到人（已经有别的模态、或 ctx 已取消）时一律按跳过处理。

第三个答案是刻意做窄的：作用域是**这个工具 + 这次会话**，两个条件都写在框里（`a bash for this session`），界面上没有"总是允许一切"这种键可写。答过之后框不再出现，但允许这件事要能看得见——所以 `cmd/atlas` 往 transcript 打一行 `bash: allowed for the rest of this session`，那是这一次授权唯一的痕迹。名单按会话记（`allowedTools` 带着会话 id），换会话、`/new`、`/resume` 之后从零开始；会话里来一次 `n` 也不影响那一次已经答过的 `a`，因为 `n` 从来不进名单。

### 模型选择

`/model` 不带参数就弹出配置里的模型让你挑，带参数才直接切换：`/model deepseek/deepseek-chat:high`。菜单每行是 `provider/model`（默认那个带 ` (default)`），右列是 `api · ctx N`——上下文预算是这里唯一能改变选择的一列，端点名字只在你配了好几个 baseUrl 时才有用；菜单开在当前模型那一行上。管道与 `-prompt` 上没有键可按，那里仍然把这张表打印出来，末尾一行 `current:` 报当前模型。`:level` 会先校验是不是思考级别，再夹到这个模型真有的轴上（拼错报错、给没有轴的模型发档位也报错），夹过就在 `model:` 行下面多打一行 `warning:`；选中的正是当前模型时不写会话记录，关掉菜单更不写任何东西。菜单把模型与档位合成一次问答：`↑`/`↓` 选模型，`←`/`→` 在这一行的档位上转（`off low high max` 到哪儿为止由模型自己声明，打开时光标停在**当前生效**的那一档上），`Enter` 一次取走两边，取到的是 `value` 或 `value:level`——和手打 `/model m:high` 走的是同一条路，`thinking_level_change` 该写就写，不写就说明没变。`←`/`→` 在两端夹取、不环绕：这是刻度不是环，从 `max` 再按一下落到 `off` 会把"更多"变成"关掉"。会话菜单没有横轴，footer 也就不提这两个键。

### 状态栏

屏幕最下面始终有一行状态，`|` 分隔六件事，顺序固定；侧边对话里最前面多一段 `side`：

```
…/pi-agent-go/go | #01a16e7d | fs+bash | deepseek-chat | think high | ctx 8.2k left
```

| 段 | 内容 |
|---|---|
| 会话种类 | 只在侧边对话里出现，值就是 `side`；主对话没有这一段（空串不进这一行） |
| 启动目录 | 会话的 cwd；在 home 下面时写成 `~/…`（与 pi 的 footer 同一条规则）。git 分支挂在后面（`…/proj (main)`），而且是在路径缩短**之后**才接上去的（0.12.20），所以分支名永远不会被切成半个 |
| 会话 | `app.Name()`；atlas 还没有 `/name`，所以实际总是 `#` + 会话 id 前八位。会话里有了第一条记录才出现（`EntryCount() == 0` 时整段不出现）——那时屏幕是欢迎页，一个 id 会替一个还不存在的会话作证，也替一个还不存在的文件作证；它和文件、和第一块 transcript 出现在同一刻 |
| agent 权限 | 授予的工具组加当前档位：`fs+bash+tasks · Ask`。左半由工具集归纳，同一包的工具并成一组（`read`/`write`/`edit`/`ls`/`find`/`grep` 是 `fs`，四个 `git_*` 是 `git`，三个 `task_*` 是 `tasks`，`-demo` 的 `echo` 就写 `echo`），右半由 `permissionModeLabel` 补上，没有权限引擎时只有左半。0.12.19 起右半是独立字段：窄到放不下左半时它单独占一格（`… | Ask | deepseek-chat | …`），档位一直在，工具摘要先走 |
| 模型 | `provider/model`，放不下时先丢掉 provider |
| 思考档位 | 下一个请求真会发出去的档位：`think high`。级别是 `off`（默认）或模型没有 `reasoning` 时整段不出现——没有档位可报，而"think off"会是这一行上最费格子的一段 |
| 上下文 | 窗口还剩下的 token：`ctx 188k left`；配置没声明 `contextWindow` 时改成已用：`ctx 12k used` |

- 宽度不够时按固定阶梯丢细节，而不是切右边缘：provider → 目录缩成 `…/vibe_project/pi-agent-go` → 会话 → 工具摘要 → 目录缩成 `…/pi-agent-go` → 目录 → 思考档位 → 只留模型、档位与上下文 → 只留档位与上下文。`side` 是唯一不按这条规则走的段：它在每一帧里都在，因为主对话里它是空串、不占格子，而一行把 `side` 丢掉的页脚会在你往侧边对话里打字时描述主对话。规则是「描述这次请求的」（模型、档位、剩余上下文）比「描述这个地方的」（目录、会话、工具集）活得久：档位是这一组里最后走的，因为模型名没了之后一个光秃秃的 `high` 什么都没说明。权限档位（`Ask` / `Auto` / `Full access` / `Off`）是这条规则唯一的例外，因为它本来就不描述"这个地方"：宽的时候它与工具摘要合写成一段，摘要走了之后它单独留着，一直到只剩上下文——它要回答的是"下一次工具调用会不会停下来问我"，看不见这一格的人不会知道这件事。0.12.18 加的三个 `task_*` 工具没有并成一组，摘要于是长到 100 列与 120 列的终端必须把整段丢掉，页脚看起来就像 atlas 没有权限档位这回事，0.12.19 把这两个字段拆开并把三个任务工具归成 `tasks`。降级顺序由调用方（`cmd/atlas`）决定，因为只有它知道哪个字段可以舍；界面只负责按给它的宽度兜底截断，所以 `tui.Config.Status` 的签名是 `func(width int) string`，拿到的宽度已经把忙碌字段扣掉了。
- 状态栏是这一帧的一段，不是 transcript 的一段：它每帧都画在最底下，滚动不带着它走，`/` 命令的输出也不会把它顶掉。
- 它占一行，这一行是从 transcript 的高度里扣的，所以 body 的行数永远是「终端高度 − 输入行 − 状态栏」。
- 忙碌字段是界面自己加的那一段，紧跟在宿主的字段后面：有 turn 在跑时是盲文点做的一格转圈（`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`，一百毫秒一轮，满一秒之后缀上秒数：`⠹ working 3s`），回合一结束就消失。转圈是重点——静止的屏幕在模型卡住时与在什么都没发生时长得一模一样，而那正是这个字段要回答的问题。宽度不够时先丢标签再丢秒数（`⠹ 3s`、最后只剩 `⠹`），帧本身不丢：一轮在跑而页脚上看不出这件事，是这一格唯一不能接受的结果。它由界面写而不是宿主写，原因是秒数需要一个时钟，而宿主只拿得到一帧——两个时钟数同一轮迟早会给出两个数。定时器只在 busy 期间存在：提交一行时起一跳，跳回来再看一眼 `busy`，回合没了就停在那里，所以空闲的屏幕不会被自己的定时器重画。输入行的提示符在忙碌时仍是静态的 `⋯`，只让页脚动：两处同时换字形会读成两个各自在忙的东西，而 `esc twice to interrupt` 那句要能读。这也是「一批消息只重画一次」那条约定还在的唯一例外，代价是忙起来每秒十次整屏重绘——所以帧率取 100ms 而不是 pi 的 80ms。
- 一轮结束时的那行收据是 transcript 里的一条 notice，不是状态栏：`1723 tokens (in 1204, out 519) · 62 tok/s · took 8.4s · ended at 15:03:11`。它由宿主写（`cmd/atlas/cost.go`），因为一轮的两个时间端点都在宿主手里，而界面每一帧都是重新算出来的、不该自己记秒表；它与忙碌标记分工明确——标记答「现在」，收据答「刚才」。写 `took` 而不是 `completed in`，因为被取消的一轮打的是同一行，而它并没有「完成」；`tok/s` 只在真的写了字时出现，空回答下面的 `0 tok/s` 看起来像故障，而它只是那一轮没有字。
- 上下文数字用的是自动压缩同一个估算（`compact.EstimateProjection`），所以状态栏说"还剩 8k"和压缩代码决定"该压了"不会互相矛盾。这个估算要遍历整个分支，而重画是每按一个键一次，所以 `cmd/atlas` 侧做了记忆化：缓存键是「会话 id + `EntryCount` + provider/model」，三者之一变了才重算。
- token 数字的格式逐条对齐 pi 的 `formatTokens`（`999`、`1.0k`、`188k`、`1.0M`、`123M`），两边读同一个数看到同一个写法。
- 权限这一段的左半是"工具集"而不是"权限等级"：没有按工具的开关，写一个 `yolo` 之类的等级就是在撒谎。授予的工具就是权限，所以状态栏直接报它。右半是 0.12.10 加的档位（`Ask` / `Auto` / `Full access` / `Off`），它不改工具集，只回答"一轮里那个 ask 由谁答"——两件不同的事，所以不合并成一个词：`fs+bash · Auto` 读作"有这些工具，其中要问的那些交给分类器"。沙箱不改变左半：`sandbox` 块是收窄而不是新增能力，它不授予任何东西。两半都由 `grantsField()` 与 `modeField()` 每帧现读，所以 `/permission` 与 `/model m1:high` 之后下一次重画就在那一行上，不需要重建状态栏。档位之所以有两个取值口而不是一个，是因为降级阶梯是逐段丢的：合并的那一份给宽帧（`fs+bash · Auto`，读作"有这些工具，其中要问的那些交给分类器"），单独的 `modeField()` 给窄帧，两个的寿命不同才可能只丢工具摘要。`/permission` 有一个复数别名 `/permissions`——参考实现的页脚就是这么写的，从那儿学来的手会照它打；别名在两张白名单之前就换成规范名，所以侧会话与归档会话放行的是它、命令面板与 `/help` 里仍然只有一行。侧会话那道"读免问、改要问"的门不在这两半里，它是侧会话自己的边界。git 分支 0.12.15 起有了：它挂在目录段后面 `…/proj (main)`，读的是 `.git/HEAD` 而不是起一个 `git` 进程——页脚每秒要问好几次，而一次进程一个帧不是状态栏会付的代价；换分支时 git 原地重写这个文件，所以比一次 mtime 与大小就够了。worktree（`.git` 是个文件、指向 `gitdir:`）与 detached HEAD（报七位短 sha）都认。0.12.20 修的是它挂上去的时机：分支名可以带斜杠（本仓库就是 `personal/dfbuddy`），而目录段是按 `/` 逐段缩短的，先拼后缩会把分支名从斜杠处切开，最窄那一档印出 `…/dfbuddy)`——一个不存在的分支名配一个不闭合的括号。现在先缩短路径再拼分支，所以每一帧要么是完整的分支名，要么整段目录退出阶梯，不会印半个名字；代价是分支名越长，最窄那一档越长，目录退得越早。

## 接入一个兼容端点

`internal/provider/openai` 讲 chat completions 协议，OpenAI 本身与 DeepSeek / Qwen / Kimi / GLM / OpenRouter / vLLM / llama.cpp 都用它：

```go
p := openai.New(openai.Compat{BaseURL: "https://api.deepseek.com/v1"})
stream, err := p.Stream(ctx, provider.Request{
	Model:    wire.Model{ID: "deepseek-chat", API: openai.APIName},
	Messages: []wire.Message{wire.UserMessage("hi")},
	Options:  provider.Options{APIKey: os.Getenv("DEEPSEEK_API_KEY")},
})
msg, events, err := provider.Collect(stream) // 事件流 + 终止消息
```

`Compat` 十一个字段：`BaseURL`、`MaxTokensField`、`RequireReasoningContent`、`DisableStreamOptions`、`TolerateMissingFinishReason`、`RequireToolResultName`、`RequireAssistantAfterToolResult`、`ThinkingDialect`、`DisableReasoningEffort`、`ToolChoice`、`SendStore`。判断某个端点要不要设它们、以及为什么不照搬 pi 的二十个开关，见 [docs/architecture.md](docs/architecture.md) 第 3 节。

- **`reasoning_effort` 对声明了 `reasoning` 的模型默认下发**（0.12.12）：`low`/`medium`/`high` 原样，`xhigh`/`max`/`ultra` 与认不出的名字落 `high`，`off` 与空级别省略——chat completions 的词汇表里没有 `"none"` 这个词。`DisableReasoningEffort` 是逃生门：有些网关认识字段名却只接受一个子集。这条路径要查 `model.Reasoning`，与 DeepSeek 方言相反。
- **`ToolChoice` 与 `SendStore` 是两个独立开关**（0.12.12）：前者原样写进 `tool_choice`（`auto`/`none`/`required`），后者发 `store: false` 而不是 `true`——这个适配器自己回放 transcript，服务端留一份就是没有任何后续请求会引用的状态。它是 opt-in，因为挡在 API 前面的一层代理可能拒绝这个字段。

## 接入 Anthropic

`internal/provider/anthropic` 讲 messages 协议，`Compat` 六个字段：`BaseURL`、`Version`、`ThinkingDialect`、`AdaptiveThinking`、`StrictTools`、`ToolChoice`。

```go
p := anthropic.New(anthropic.Compat{BaseURL: "https://api.anthropic.com/v1"})
stream, err := p.Stream(ctx, provider.Request{
	Model:    wire.Model{ID: "claude-sonnet-4-5", API: anthropic.APIName, Reasoning: true},
	Messages: []wire.Message{wire.UserMessage("hi")},
	Options: provider.Options{
		APIKey:        os.Getenv("ANTHROPIC_API_KEY"), // 走 x-api-key，不是 Bearer
		ThinkingLevel: "medium",                       // → budget_tokens 8192
	},
})
msg, events, err := provider.Collect(stream)
```

- `max_tokens` 是必填字段，所以有三级兜底：`Options.MaxTokens` → `model.MaxTokens` → 4096。接上目录之后几乎总走第二条。
- 思考预算按级别映射（low 2048 / medium 8192 / high 16384，`xhigh`、`max` 与 `ultra` clamp 到 high——真实 Anthropic 的预算式思考没有 high 以上的档），实际下限被 `max_tokens - 1024` 夹住；**预算不足 1024 时不发 thinking 字段**，而不是发出去让 API 报 400。thinking 开启时不发 `temperature`。
- `ThinkingDialect: "deepseek"` 时这条预算路径整段让位：不发 `budget_tokens`，改发 `thinking: {"type": ...}` 加 `output_config: {"effort": ...}`；`off` 只发 `{"type": "disabled"}`，空级别两个字段都不发（厂商默认开着、high）。关闭方式不是猜的：官方表格里开关那一格跨 OpenAI 与 Anthropic 两列合并，兼容页也写明 Messages 端点支持 `thinking`、忽略 `budget_tokens`（见「配置」一节的 `compat.thinkingDialect`）。方言为空时预算路径一个字节都不变，`ThinkingDialect` 也只在这一个字段上分叉。
- system 只有一个顶层字段：transcript 里的 system 消息被重放成一条提示词，消息数组里不再出现 system 角色。
- 连续的 tool result 合并成一条 user 消息；带图片的结果用块数组，纯图片时补 `(see attached image)`。
- 思考块必须带回签名：带签名的回填成 `thinking`，密文思考回填成 `redacted_thinking`，**缺签名的降级成普通文本**（缺签名回放会被 API 拒绝）。
- **`AdaptiveThinking` 是第三条思考分支**（0.12.12）：打开后不发 `budget_tokens`，改发 `thinking: {type: "adaptive", display: "summarized"}` 加 `output_config: {effort}`，模型自己决定想多少。级别映射照 pi——`low` → low、`medium` → medium、其余一律 high（high 以上的 effort 只在少数新模型上可用）。`off` 在这里有真正的写法 `{"type": "disabled"}`。它是 opt-in：一个不认这个 `type` 的老模型是整单 400，不是忽略。
- **`StrictTools` 要两个条件同时成立**（0.12.12）：读者说这个端点支持（`Compat.StrictTools`），工具作者说这个 schema 适合（`wire.Tool.Strict`）。再加三道守卫——不支持的关键字命中任何一个、缺少 `additionalProperties: false`、一份请求超过二十个 strict 工具，都是官方写明的 400 条件，处理方式是那个工具**整份降级为不严格**，而不是删掉那个关键字（删掉会让约束对模型静默消失）。
- **`ToolChoice` 在这一家的词汇不同**：共享的三值里 `required` 在这里拼作 `any`，且只在有工具时发。

未做的请求侧字段（`anthropic-beta` 头、`eager_input_streaming`、OAuth 身份头）见 [docs/architecture.md](docs/architecture.md) 第 9 节。

## 接入 OpenAI Responses

`internal/provider/responses` 讲 Responses API（`api` 写 `openai-responses`），也就是 OpenAI 现在的 `/v1/responses`。它与 chat completions 是两家协议，`Compat` 五个字段：`BaseURL`、`ToolChoice`、`ServiceTier`、`Verbosity`、`Truncation`。

```go
p := responses.New(responses.Compat{BaseURL: "https://api.openai.com/v1"})
stream, err := p.Stream(ctx, provider.Request{
	Model:    wire.Model{ID: "gpt-5", API: responses.APIName, Reasoning: true},
	Messages: []wire.Message{wire.UserMessage("hi")},
	Options: provider.Options{
		APIKey:        os.Getenv("OPENAI_API_KEY"), // Bearer
		ThinkingLevel: "medium",                    // → reasoning: {effort: "medium", summary: "auto"}
	},
})
msg, events, err := provider.Collect(stream)
```

- **无状态**：`store: false` 恒发（这个 API 的默认值是 `true`），不带 `previous_response_id`，也不带 `conversation`。上下文只有一条通路——transcript 整个放进 `input`。所以会话的真相只有一个，就在本地 JSONL 里。
- 代价是 reasoning item 必须自己带回来：思考在跑时恒发 `include: ["reasoning.encrypted_content"]`，而 assistant 的思考块把整段 item 存进 `ThinkingSignature`，下一次请求原样送回去（不按已知字段重建，否则 `encrypted_content` 会被静默丢掉）。`off` 时发 `{"effort": "none"}` 而不是省略字段——这个 API 默认 medium，省略等于把思考打开。
- system 提示词是 `input` 的第一项（reasoning 模型用 `developer` 角色，其余用 `system`）；用户内容用 `input_text` / `input_image`，assistant 的内容用 `output_text`，工具调用与结果用 `function_call` / `function_call_output`。
- `prompt_cache_key` 取会话 id：省钱的机制挂在会话上，不挂在客户端状态上。思考在跑时不发 `temperature`（这里是被拒绝的组合，不是被忽略的）。
- `max_output_tokens` 有下限 16：低于它的值会被 API 拒绝而不是钳制。
- **四个可选的请求旋钮，空值一概不发**（0.12.12）：`ToolChoice` 写 `tool_choice`（这家与 chat completions 拼法相同），`ServiceTier` 写 `service_tier`（`auto`/`default`/`flex`/`priority`，用延迟换价格），`Truncation` 写 `truncation`（`auto`/`disabled`，超长时服务端能不能丢掉开头），`Verbosity` 写进 `text: {verbosity}`（`low`/`medium`/`high`，回答里带多少散文）。四个都是字符串直传，`config` 只做取值校验。需要说清来源：pi 的参考实现里只有 `service_tier`，另两个的形状是照 OpenAI 公开字段自己补的。
- 未做的请求侧字段（`parallel_tool_calls`、`max_tool_calls`、内置工具、`input_file`、服务端上下文）见 [docs/architecture.md](docs/architecture.md) 第 9 节。

## 接入 Gemini

`internal/provider/google` 讲 Gemini 的 generative language API（`api` 写 `google-generative-ai`），也是唯一把「模型名」和「要流」放进 URL 的一家：

```go
p := google.New(google.Compat{BaseURL: "https://generativelanguage.googleapis.com/v1beta"})
stream, err := p.Stream(ctx, provider.Request{
	Model:    wire.Model{ID: "gemini-3-pro-preview", API: google.APIName, Reasoning: true},
	Messages: []wire.Message{wire.UserMessage("hi")},
	Options: provider.Options{
		APIKey:        os.Getenv("GOOGLE_API_KEY"), // 走 x-goog-api-key，不是 Bearer
		ThinkingLevel: "high",                      // → thinkingLevel: "HIGH"
	},
})
msg, events, err := provider.Collect(stream)
```

- **端点形状是 `POST {base}/models/{id}:streamGenerateContent?alt=sse`。** 版本段（`/v1beta`）在路径里而不是头里，模型名与 `streamGenerateContent` 是路径的一部分，`alt=sse` 是查询参数。这是 `provider/conformance` 需要 `ModelInPath` / `StreamInPath` 两个开关的原因——审计仍然要求 model 与「要流」被表达出来，只是知道它们该去路径里找。
- **思考有两种方言，按模型 id 嗅探。** `thinkingLevel`（`LOW`/`MEDIUM`/`HIGH`）是 Gemini 3 与 Gemma 4 那一代的粗档位，`thinkingBudget`（token 数）是老模型那种额度，预算 low 2048 / medium 8192 / high 16384 / `xhigh` 以上取 24576。`Compat.ThinkingDialect` 写 `"level"` 或 `"budget"` 可以覆盖嗅探——网关把模型改名之后，id 是唯一剩下的线索。
- **`off` 在这里是真正的关闭**：`thinkingBudget: 0`，不是省略。思考打开时带 `includeThoughts: true`，否则思考摘要不进流。
- **思考与 `temperature` 在这里不互斥**：另两家的协议拒绝这个组合，Gemini 接受并存，所以这里没有那条分支——照着别家抄一条没有依据的限制，只会让本来能调的温度发不出去。
- **工具 schema 只发 `parameters`**（OpenAPI 3.0 子集），不是 `parametersJsonSchema`：atlas 的 schema 全在子集里，而一台只认 `parameters` 的网关是真实存在的失败模式。
- **工具调用的 id 在解码时合成**（`name_N`，这个协议通常不给 id），请求侧只做清洗（`[A-Za-z0-9_-]`，截到 64）。反过来在请求侧合成是不行的：同一轮的多个调用会共用同一个 id，而它们的签名与结果各不相同。
- **思考签名只在同 provider、同模型、且是合法 base64 时回放**：`thoughtSignature` 绑在生成它的那个模型上，给另一个模型等于塞一段它不认识的字节。其余情况降级成普通文本。
- 缺口见 [docs/architecture.md](docs/architecture.md) 第 9 节。

## 跨适配器的一致性用例

`internal/provider/conformance` 不属于任何一家适配器，它写的是"每个适配器都必须守住的承诺"：终态之后 `Recv` 给 `io.EOF`、首事件是 `start`、恰好一个终态且是最后一个、每个块的 delta 之和等于块正文、块编号从 0 按开块顺序递增、`partial` 不会在事后被改写、同一个 `Provider` 上两条并发流结果一致，请求侧则只查语义（model、prompt、每个工具名、`stream`、key 在头里）。场景由适配器自己的 `conformance_test.go` 提供。

它抓到的第一条真 bug 是 `anthropic` 的：`content_block_start` 里已经带的文本被丢掉，而 TUI 是按 delta 累加的——那块文字会凭空少几个字。

## 工具集

`internal/tool/fs`、`internal/tool/shell` 与 `internal/tool/web` 都是纯 `tool.Tool` 实现，注册进 `Registry` 即可用：

```go
registry := tool.NewRegistry()
for _, t := range fs.Tools(fs.Options{Cwd: workdir}) {
	registry.Add(t)
}
for _, t := range shell.Tools(shell.Options{Cwd: workdir}) {
	registry.Add(t)
}
for _, t := range web.Tools(web.Options{APIKey: key}) {
	registry.Add(t)
}
```

`fs.Tools` 返回 read / write / edit / ls / find / grep：路径相对 `Cwd` 解析，写操作按绝对路径串行化，`find` / `grep` 是进程内实现（不依赖外部 `fd` / `rg`），gitignore 支持常用子集。`shell.Tools` 返回 bash：前台无默认超时（靠 `context` 取消；后台按给的 `timeout`，没给是一小时），取消与超时杀整个进程组，输出按尾部截断并把全量写进临时文件；`Options.Sandbox` 非 nil 时每条命令先过一遍 `Wrap`，它报错就整条不跑。`web.Tools` 返回 websearch / webfetch，凭据为空时返回 `nil`（不是两个注定失败的工具）。

0.12.18 起多一个来源：`internal/tool/tasks.Tools(service)` 返回 `task_query` / `task_output` / `task_stop` 三个工具，它们读的是 `internal/task` 那份记录，见下面「后台任务」。

MCP 的远端工具也满足同一个接口，但不是 `internal/tool` 产出的：`internal/mcp` 在启动时把每台服务器的声明包一层，注册进同一张表，见下面「MCP」。

这些工具的能力边界（图片不缩放、gitignore 子集、grep 用 RE2、Windows 树杀限制、读不到需要登录的页面、沙箱只在 macOS 上且只包住 bash 的子进程等）见 [docs/architecture.md](docs/architecture.md) 第 9 节。

### 联网

`websearch` 与 `webfetch` 是 0.5.0 新加的两个工具，默认随 `web` 块一起出现，用户没有自己的入口——只能写一句话让 agent 去用，工具行照常出现在 transcript 里（那一行也是唯一的账）。

- **两步，不是一个开关。** 搜索回答"有哪些来源"，抓取回答"这一篇说什么"。搜索**不返回正文**（`need_content` 恒不上送），所以模型必须先用每条的 `score`（0–3，1.5 以上算好）与 `authority`（0–4，`?` 表示没标注）挑一条，再抓那一条。十条整页塞进上下文是这条链上最贵也最没用的做法。
- **查询支持 `site:`、`-site:`、`"精确短语"`、`-词`、`lang:zh,en`、`after:`、`before:`。** 它们是"大宗调研"的关键：`"CVPR 2026" site:openaccess.thecvf.com` 与 `site:arxiv.org lang:en` 比抓十个页面再自己过滤便宜得多。
- **抓取的正文套一层 `<page url="…">`。** 页面是这里唯一由陌生人写的东西，包一层是在说"这段是资料，不是给你的指令"。它挡不住真正的注入，但边界得在文本里。
- **计费按次，不按 token**：每次成功请求 5 元 / 1000 次（空结果与失败不计费），所以一轮里搜几次、抓几次就是这一轮的钱。限速与错误分类照官方文档：408 / 429 / 5xx 自动重试一次，其余 4xx 直接报；402 会明说"余额"，401 指向 `web.apiKeyEnv`。
- **读不到需要身份的东西**：页面是端点去抓的，不是本机。`localhost`、内网、私有仓库、付费墙都不可达——要看本机跑起来的服务只能走 `bash` 加 `curl`（那条路要审批）。
- **会出网**：`query` 与 `url` 会发给端点。`ori_question`（把用户原话一并递过去）与 `localization`（要的是 IP 地址）都没有暴露。

### 沙箱

`sandbox` 块（0.6.0）把 `bash` 起的子进程树关进内核策略里：写只允许工作区与 `$TMPDIR`，`.git/hooks` 与已存在的 `.git/config` 被拒，网络按 `"network"` 决定。

```json
{"sandbox": {"network": false}}
```

- **执行器是内核的，我们只拥有 policy。** 包里一行 syscall 都没有：交给系统 `/usr/bin/sandbox-exec` 的是一份文本（SBPL 的白名单，`(deny default)` 打底）。用户态拦截只能约束听话的程序，而 agent 恰恰会产出不听话的命令——一条 `curl` 不经过我们的代码，一个静态链接的二进制直接 syscall。代价是这个执行器从 2013 年就标着 `(DEPRECATED)`，所以它被收在一个 `defaultExecutor()` 后面：换 `container` 或 VM 时，上层 policy 一行不改。
- **`git commit` 必须成功。** 这是第一版最重要的一条验收，也推翻了"让 git 写失败"那个初稿：让 git 写失败只能把 `.git` 整块拒掉，那样 `add` / `stash` / `checkout` 也没了，人会直接把 `sandbox` 块删掉。所以拒的是两个具体的点——`.git/hooks`（无条件，连创建也拒；配套把 `GIT_TEMPLATE_DIR` 指到一个空目录，否则 `git init` 会失败）与已存在的 `.git/config`（`git init` / `git clone` 要能建它，所以只在它已经存在时拒）。
- **每个路径都过 `EvalSymlinks`。** `/tmp` 与 `$TMPDIR` 在 macOS 上都是符号链接（`/private/tmp`、`/private/var/…`），profile 里写未解析的路径等于"沙箱开着但什么都写不进去"。
- **拒绝清单每次包裹时现场算**，不是启动时算一次：`git init` 会在工作区里造出新 `.git`，而它正是要拒的东西。扫描只进目录，跳过 `.git` / `.vscode` / `.idea` / `node_modules`，深度默认 3。
- **只能套一层。** 已沙箱化的进程里再跑 `sandbox-exec` 会得到 `sandbox_apply: Operation not permitted`，所以 `Wrap` 看见 `ATLAS_SANDBOXED=1` 就原样返回。
- **失败是错误，不是放行。** 没有执行器时启动只打一句 `warning:`，之后每条 bash 命令报错不执行，错误里写着"要么装上执行器，要么删掉 `sandbox` 块"。把"沙箱没套上"降级成"那就不套"，等于配置里写着 `sandbox` 而实际拥有整台机器。
- **它不管什么**：只包 `bash` 的子进程（`write` / `edit` 是进程内 `os.WriteFile`，靠工具自己的路径规则）；读完全没有隔离（`~/.ssh` 在 bash 里读得到）；网络全有或全无，没有域名白名单；只在 macOS 上，Linux 有设计没实现，Windows 没有。

完整依据——srt 与 MiniMax Code 的实现抄录、SBPL 骨架、本机实测的四个坑、Linux 的 bwrap argv 与 Windows 的 WFP——见 [docs/sandbox.md](docs/sandbox.md)。

### 后台任务

`bash` 的 `run_in_background`（0.12.18）把命令交给一个新的进程组，调用立刻返回一个 `bg_` 加十二位十六进制的编号；之后用 `task_query` 看它到哪儿了、`task_output` 从某处接着读输出、`task_stop` 让它停。三个工具与 `bash` 由同一个服务接起来，记录写在会话目录的 `tasks.jsonl`。

- **任务是行，不是 goroutine。** 谁跑进程谁写结局，服务只做状态转换与读盘——它甚至不留内存列表，每次查询都读文件。这样崩溃之后记录仍然自洽，而"有个后台任务"在 `/resume` 回来时也还是能看见的那一行。
- **输出走文件，不走管道。** 管道需要一个读者，而读者随启动它的那次调用返回就消失了，剩下的命令会卡在满缓冲区里永远不动。所以 stdout 与 stderr 一起重定向到一个文件，`task_output` 拿 `offset` 去读它。
- **`ctx` 不传下去。** 后台的意思就是"启动它的那次调用结束之后它还活着"：Ctrl+C 打断一轮对话不该顺手杀掉半小时前起的构建。
- **默认一小时封顶。** 给了 `timeout` 就按给的来，没给按一小时；到点先 `SIGTERM`，半秒后 `SIGKILL`，杀的是整组。
- **能力按平台存在。** 非 unix 上 `bash` 的 schema 与描述与 0.12.17 逐字节相同，三个任务工具一个都不注册——不是报错，是这里没有这件事。
- **记录与进程同生共死。** 退出时 `Close` 收掉本进程名下的活跃任务组；进程被别人杀掉之后，`task_query` 按 `kill(pid, 0)` 把那一行读成 `lost`（只改回答，不改文件——写回等于替一个可能还活着的进程做主）。跨重启的接管属于 daemon 那种进程模型，这一版不假装有。
- **只有 `bash` 一种。** `subagent` / `workflow` / `custom` 在 `task.Kind` 里是声明而不是实现：子代理要先有"另一个 agent 能被告知、能被打断、能回一份结果"这件东西，那条在 [docs/architecture.md](docs/architecture.md) 第 9.3 节记着。

### MCP

MCP 客户端（0.7.0 起；OAuth 与动态注册在 0.9.0 补上，长任务的 `GET` 长连流与 progress 在 0.10.0，工具表的重列与 `tools` 白名单在 0.12.11）把别的进程里的工具变成模型看得见的工具：两条传输（stdio 起子进程，Streamable HTTP 打 `POST` 并开一条 `GET` 长连流），握手钉在 2025-11-25 那一代，远端工具原样注册成本地 `tool.Tool`。配置是独立的一份文件，不塞进 `config.json`——它是"要在这台机器上执行哪些程序"，和"请求怎么发"不是同一类设置。

```json
{
  "mcpServers": {
    "fs": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/Users/me/work"], "env": {"TOKEN": "${MCP_TOKEN}"}, "tools": ["read_file", "list_directory"]},
    "docs": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"}},
    "issues": {"type": "http", "url": "https://issues.example.com/mcp", "auth": "oauth", "scopes": ["mcp:read"]}
  }
}
```

- **两级文件，项目级只许 `enabled`。** 全局 `$ATLAS_HOME/mcp.json`，项目 `<cwd>/.atlas/mcp.json`（与主题同一个 `.atlas` 目录）。项目文件能写 `command` 就是仓库里的代码执行，所以 `command` / `url` / `env` / `headers` / `cwd` / `timeout` / `auth` / `scopes` / `tools` 出现在项目级直接报错——atlas 没有 pi 的"项目被信任"那一套，不打算为一个能跑任意 argv 的键临时发明一套。项目条目写的名字必须在全局里存在，静默忽略会让"我明明写了"变成一次猜谜。
- **`type` 可省**：有 `url` 就是 http，有 `command` 就是 stdio，都有或都没有就报错。`"type": "sse"` 明确拒绝并指向流式 HTTP 端点（通常以 `/mcp` 结尾）——老的 HTTP+SSE 传输在 2025-03-26 就被换掉了，两个都支持等于维护两套会话状态机，而现有服务器的新端点本来就在。
- **`${NAME}` 从环境变量展开**，未设时保留字面量并打一句 note。不是"悄悄变成空串"：`Bearer ` 会照常发出去，然后 401 报在服务器那头，读的人会去查 token 而不是查变量名。
- **`auth` 必须写出来，`401` 不会替你启动登录。** `"auth": "oauth"` 是一句声明：连这台要授权。不做试探，否则"这台要不要登录"就取决于它今天返回什么。没登录过时启动一个请求都不发，`/mcp` 里那一格写 `needs login`，表下给出 `/mcp login <server>`。那个命令打印一个 authorize URL（不自动开浏览器）、在 `127.0.0.1` 的随机端口上等回调，浏览器里点完就把 client、token 与发现到的端点写进 `$ATLAS_HOME/oauth.json`（0600、tmp + rename）。等 5 分钟就放弃，`ssh` 过去的机器要自己把那个端口转出来——提示里带着 `ssh -L` 的写法。`scopes` 可选，写了就连 `resource` 一起作为参数发出去；含 `offline_access` 时自动补 `prompt=consent`——不补就拿不到 refresh token，下次启动又要点一次浏览器。服务器少给一个 scope 不会在登录时当场报错，它会以调用被拒的形式出现，而那条路最后归到"再登录一次"。
- **401 只换一枚 token，不换一个人。** 401 时刷新一次再重试一次，最多一轮；403 不刷新（那是权限不是 token），直接归到"再登录一次"。同一时刻的多个 401 共用同一次刷新——否则并发里的第二个请求会拿着刚换来的 token 再刷一遍，而刷新 token 常常是一次性的。刷新失败就是"再登录一次"，没有后台定时器，`expiresAt` 只作存档信息。登录之后那台的工具要重开 atlas 才出现——工具表在进程启动时装配，换个会话不够，完成报告里就写着这一句。
- **`/mcp` 原来是一张只读的账，现在多一个动词。** `/mcp` 一张表 `SERVER VERSION TOOLS STATE`，失败原因打在表下面而不是表里（错误要多长有多长，撑宽的列会把读者来看的列挤下去）；`/mcp <server>` 再列那一台的工具，`TOOL DECLARED AS DESCRIPTION`——`DECLARED AS` 才是模型真正看到的名字。`login <server>` 是唯一的写入，理由很实际：`auth` 的失败只在 `/mcp` 里看得见，而"看见之后能修"比"命令纯洁"值钱。登录在侧会话与归档会话里被拒——它会改磁盘上属于所有会话的共享状态。重连与编辑仍然没有：每一种动词都得先有个名字，而那个问题还没问完。
- **连不上只警告，不拦启动。** 一台服务器挂了只丢它自己的工具，`warning:` 里带着它 stderr 的尾巴（截成一行）——只写 `connection closed` 会把读的人送到错的地方。配置文件本身写错是错误：那份文件是用户写的，能改。
- **名字是 `mcp__<server>__<tool>`**：非 `[A-Za-z0-9_]` 折成 `_`，超长或撞名加 8 位 hash（由 `server\0tool` 算出），仍撞再加 `_`。远端名与本地名的对应关系在 `/mcp <server>` 里直接看得到，所以折叠不必可逆；用计数器代替 hash 就会随配置顺序改名，而"昨天能跑的工具今天不见了"是最难查的一类。
- **结果照内容块走**：text 变文本块，image 变图片块，audio 与 resource 的二进制落临时文件加一行说明，文本超 20KB 从中间截断并把全文写进另一个临时文件。`structuredContent` 与 `isError` 原样落位。工具调用**不重试**——`tools/call` 不是幂等的，唯一允许的重试是"带着 session id 的 404"（服务器忘了这个会话，那个请求它从没执行过）。
- **长任务看进度，`timeout` 是空闲上限。** 语义换了：原来是"这次调用最多花这么久"，现在是"这么久没有消息就放弃"，每条 `notifications/progress` 都把时钟往后推，而一条真的在干活的调用可以一直跑下去——想停按 Esc。两条通道都得接：进度可能内联在 `POST` 自己的答案流里，也可能走 `GET`（握手之后开一次，`405` / `401` 与坏事件一律静默，这条路承载的全是"没有也能活"的信息）；只接一条是这类客户端最常见的错。进度也画到了屏幕上：`tool_execution_update` 写进那条调用行的末尾，**覆盖同一行**而不是一行一条，250ms 一次、消息折成一行并截到 60 字，结果落地时这个尾巴让位给"几行输出 · 几秒"（`internal/tui/events.go` 的 `updateTool`）；`--json` 与 `-rpc` 的消费者拿到的仍是每一条原始事件。
- **stdio 服务器不套沙箱，也不该套。** 沙箱的边界是"谁写的命令行"：`bash` 被约束，因为命令行是模型写的；`mcpServers` 不被约束，因为它是用户写的，和 `atlas` 自己的二进制同级。套上也基本不能用：`npx` 要写 `~/.npm`、要联网装包。所以**装一个 MCP 服务器等于把你给的权限给它**，agent 只是多了它的工具。真正约束它的是暴露范围——一个服务器能干什么由它自己的代码决定，客户端只能决定模型看不看得见它。
- **工具表在会话中途会变，两个数字都得留着。** 服务器发 `notifications/tools/list_changed` 就重列（0.12.11），后加的工具下一轮就能调：处理必须 `go` 出去，因为通知到达的 goroutine 就是读这条连接的那一个，在同 goroutine 里发 `tools/list` 会等一个只有自己能投递的答案；启动窗口里「通知比 hook 先到」由 `client.watch` 的返回值补上，重列期间再来的通知并成一次而不是排成一队各自拉一遍。每台服务器可以写 `tools` 白名单（按工具名、按那台自己的拼写、项目级不许写），于是 `/mcp` 的 TOOLS 列写 `2 of 5` 而不是 `2`——没有 M 的 N 分不清「只有两个」与「被筛到两个」。白名单指名了服务器没有的工具只警告不报错：宽严是读者的决定，而打错要能看见。重列失败保留旧表并把原因打在表下，一台还连着的服务器不会因为一次列表失败变成 `failed`。
- **不做**：resources / prompts / sampling / elicitation（入站请求一律回 `-32601`）、`ping`。OAuth 只做"本地单二进制能走通"的那一条：动态注册（DCR）加授权码加 PKCE 加刷新，回调落在 `127.0.0.1` 的随机端口。step-up、client credentials 与 CIMD 不做——前两个要另一套知识，第三个要一个域名；`client_secret_post` 专门报一句错，因为"悄悄换一种方式"会把"这台为什么登录不了"变成一次读源码。`ping` 没做的原因最值得记：它只回答"这条连接还活着"，而这里每一处需要知道这件事的地方已经有答案——要么正握着那把锁，要么已经超时。`GET` 那条流在带 session 的 `404` 重建之后不再开（`streamOnce` 只放一次，旧流属于一个已经被忘掉的会话），那之后进度只剩 `POST` 内联这一条。

协议取舍、数据形状、13 条落地时被实测推翻的判断见 [docs/mcp.md](docs/mcp.md)（长任务那一轮记在它第 12 节：`GET` 流、progress、`timeout` 改成空闲上限，以及三处被实测推翻的估计）；OAuth 那一半的规范依据、9 条验收与 8 处落地记录见 [docs/mcp-oauth.md](docs/mcp-oauth.md)；缺口的完整清单在 [docs/architecture.md](docs/architecture.md) 第 9 节第 9 条。

## 会话存储

`internal/session` 读写 pi 的 v3 JSONL 会话。格式是兼容性契约，所以条目编解码是手写的：未修改的行按原始字节回放，pi 写出的文件读进来再写出去字节不变。

```go
manager, err := session.ContinueRecent(workdir, "") // 或 session.Create / session.Open / session.InMemory
if _, err := manager.AppendMessage(wire.UserMessage("hi")); err != nil {
	return err
}
ctx, err := manager.BuildContext() // messages + thinkingLevel + model
```

- 文件懒创建：只有出现 user 或 assistant 消息才落盘，改设置或加标签不会留下空会话——`/new` 与 `/resume` 也不会替被离开的会话补写，所以目录里不会攒下一行一个的空壳。
- 树与分支：`Branch(id)` 移动叶指针后追加即开新分支，`ResetLeaf()` 让下一条成为新根，历史永不重写。
- 上下文投影：最新 compaction 顶替更早历史，`firstKeptEntryId` 起的那段保留，保留区间里的 system 消息丢弃；`context_edit` 在这之上改写单条消息。
- 会话专有角色（`custom`、`branchSummary`、`compactionSummary`、`bashExecution`）在投影时转成 user 消息，包装文案逐字对齐 pi。
- `CreateBranchedSession` 把一条分支导出成独立会话（剥掉标签并重排 parentId），`ForkFrom` 把会话复制到另一个项目目录。
- `List(dir, cwd)` / `ListArchived(dir, cwd)` / `ListAll(agentDir)` / `ListArchivedAll(agentDir)` / `FindMostRecent` / `FindByID` 支撑 `/resume`、`/recover`、`/resume --all`、`/recover --all` 与 `-continue`；cwd 过滤就一条 `samePath` 谓词，`List` 与 `FindMostRecent` 共用，归档判断只在 `listSessions` 里一次。

已知边界（`bashExecution` 只读、`listAll` 与进度回调未做等）见 [docs/architecture.md](docs/architecture.md) 第 9 节。

## 编排

`internal/app` 把会话绑到一个 agent 上，是前端唯一驱动的对象：

```go
runtime, err := app.New(app.Config{
	Model:        model,      // wire.Model
	Provider:     adapter,    // openai.New(...) 或任何 provider.Provider
	SystemPrompt: prompt,
	Tools:        tools,      // fs.Tools + shell.Tools
	Resolver: func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		// 会话里只存名字，这里把它们还原成描述符 + 适配器
	},
}, manager)

result, err := runtime.Ask(ctx, wire.UserMessage("hi"), func(event wire.AgentEvent) error {
	return nil // 渲染事件；到达这里时消息已经落盘
})
fmt.Println(result.Text(), result.StopReason, result.Usage.TotalTokens)
```

- 落盘时机是 `message_end`：每条消息一完成就写进会话，崩溃只丢未完成的那条。
- 恢复时以会话为准：模型取最后一次 `model_change`，思考级别取最后一次 `thinking_level_change`，系统提示词取已存的 system 消息。恢复不了时用配置模型，原因在 `RestoreNote()`。
- `SetModel` / `SetThinkingLevel` 写 `model_change` / `thinking_level_change`（只在真的变化时写），名字走 `SetName`。
- 一次 turn 独占：运行中再 `Ask` 或切换模型返回 `ErrBusy`；`Steer` 例外，它在 turn 进行中把消息插进下一轮请求。
- `Branch(id)` 把叶指针移回历史节点并重投影 transcript，模型与思考级别不变。

侧会话（对应 MiniMax Code 的 `/btw`、`/side`）是临时分支：

```go
side, err := runtime.BeginSide()          // 从主会话当前 leaf 拍只读快照
result, err := side.Ask(ctx, wire.UserMessage("这个函数为什么这么写"), emit)
runtime.EndSide()                        // 关闭即丢弃；BeginSide 会替换旧的
```

侧会话用独立 agent 跑，主任务不中断；它共享主会话的模型、思考级别、工具与钩子，消息存进内存会话，所以不会出现在 `/sessions` 列表里，也不留文件。只读约束写进侧会话的 system prompt：改不改由提示词约束，而不是让工具知道自己被谁调用。

## 上下文压缩

`internal/compact` 决定从哪里切断 transcript、把丢掉的那段交给模型摘要；`internal/app` 负责落盘与事件。手动压缩、阈值压缩与溢出恢复共用同一条事件流（`compaction_end.willRetry` 只有溢出恢复可能给 `true`）：

```go
result, err := runtime.Compact(ctx, "", func(event wire.AgentEvent) error {
	return nil // compaction_start / compaction_end
})
fmt.Println(result.TokensBefore, *result.EstimatedTokensAfter)

runtime.SetAutoCompaction(false) // 默认开启，阈值是 contextWindow - reserveTokens
```

默认设置是 `compact.DefaultSettings`（开启，reserve 16384，keep 20000），可以用 `app.Config.Compaction` 换成别的值。

- 切割点只落在 user 或 assistant 条目上：toolResult 不能当边界，否则下一次请求里会多出一个孤儿块。预算越界落在某个 turn 中间时做 split turn，历史与 turn 前缀各摘要一次，用 pi 的 `\n\n---\n\n**Turn Context (split turn):**\n\n` 连接。
- 分支上已有 compaction 时走 UPDATE prompt 并继承文件清单；否则每次压缩都会丢掉上一份摘要已提炼的内容。
- 摘要失败一律不落盘：`error` / `length` / 模型发起工具调用 / 空正文都算失败，原因写进 `compaction_end.errorMessage`；不重试摘要请求本身，也不失败已经完成的 turn。
- 估算不引 tokenizer：ASCII 计 1 字符、非 ASCII 按 UTF-8 字节计，再除以 4。英文比例与 pi 一致，中文约 0.75 token/字（pi 的 UTF-16 长度会低估 4 倍）；图片固定按 4800 字符计。
- 自动压缩在两个时刻判断：每轮结束后（阈值原因），以及**每次发请求之前**（准入校验）。后者补的是被中断的轮次——过去只在 `err == nil` 时才压，所以停在一半的分支会一直停在高水位上。发请求前的那一次把待发的 prompt 也算进估算：过阈值但仍在窗口内就压完照发（请求还装得下，摘要失败不该拒活），过窗口就压，压不下去就不发，返回一句带数字的话。
- 请求被厂商判为超限时有且只有一次恢复：把失败的那条助手消息用 `context_edit` 置为 `null`（它还在文件里，只是不进投影）、以 `overflow` 原因压缩、然后用**空 prompt** 重跑——起始那条 prompt 还在上下文里，再追加一次等于把同一个问题问两遍。第二次仍溢出就不再压，改发一句可行动的话。判据是一个随消息走的错误码 `wire.Message.ErrorCode`，不是读英文：`provider.Frame.Error` 给每条失败打码，所以四家适配器都不必各写一份。
- `contextWindow` 未声明（0）时不触发自动化，准入与恢复也一起关掉；手动 `Compact` 要求会话空闲（RPC 的 `compact` 会先 abort 正在跑的 turn）。开关只在进程内，续跑回到配置默认。

细节（usage 基线为什么必须看分支位置、`omitted` 检查、预算分配、溢出恢复的顺序与准入的两条线）见 [docs/architecture.md](docs/architecture.md) 第 3 节；与 pi 的差异见第 8 节第 23–27 条。

## RPC

`internal/rpc` 在 stdin/stdout 上讲 pi 的 JSONL 协议：一行一条 JSON，响应与事件共用同一条流。

```bash
./atlas -rpc                  # 交互：每行一条命令，Ctrl-D 结束
# 或者从管道喂命令：
( printf '{"id":"1","type":"get_state"}\n'; printf '{"id":"2","type":"prompt","message":"hi"}\n'; sleep 1 ) | ./atlas -rpc
```

```go
srv := rpc.New(runtime)
err := srv.Serve(ctx, os.Stdin, os.Stdout) // 读到 EOF 结束
```

- `{"type":"response", ...}` 是命令应答，其余 `type` 是 agent 事件。`prompt` 先回 `{"disposition":"started"}`（流式中入队则是 `queued`），再开始发事件。
- 已实现 28 个命令：`prompt`、`steer`、`follow_up`、`abort`、`clear_queue`、`new_session`、`switch_session`、`fork`、`clone`、`get_fork_messages`、`get_session_stats`、`get_state`、`get_messages`、`get_last_assistant_text`、`get_entries`、`get_tree`、`set_model`、`cycle_model`、`get_available_models`、`compact`、`set_auto_compaction`、`set_thinking_level`、`get_available_thinking_levels`、`cycle_thinking_level`、`set_steering_mode`、`set_follow_up_mode`、`set_session_name`、`get_commands`、`extension_ui_response`。
- 其余五个（`set_auto_retry`、`abort_retry`、`bash`、`abort_bash`、`export_html`）回 `success:false` 与 `<cmd> is not implemented by this kernel`，不返回假数据。清单与前提见 [docs/architecture.md](docs/architecture.md) 第 8 节。
- turn 进行中仍可收 `steer`、`abort`、`get_state`：命令逐条串行处理，只有 prompt 的 turn 跑在后台 goroutine 里。

## 与 pi 的契约

- `Message`、`Content`、`Usage`、`AssistantEvent`、`AgentEvent` 的 JSON 字段名与 pi 一致，可直接读写 pi 的会话条目。
- 内容块可以是 JSON 字符串或块数组；两种形状都会被保留，写入时不会改变原形状。
- `contentIndex` 使用指针，确保索引 `0` 不会被 `omitempty` 丢弃。
- 事件顺序对齐 pi：
  - 并行工具：先按源顺序发 `tool_execution_start`，`tool_execution_end` 按完成顺序，工具结果消息最后按源顺序发。
  - 顺序工具：start/end/结果消息逐个交错。
  - `stopReason: "length"` 时不执行工具，全部转成错误结果。
  - abort 产生 `stopReason: "aborted"` 的 assistant 消息，并以 `agent_end` 收尾；已经发出的正文留在消息里（0.12.16 起，此前整条消息是空的）。

### 有意保留的差异

- `wire.NormalizeContext` 不会在已有 system 消息前再插一条；Go 侧的 transcript 在内存里就是发送形状。
- 系统提示词不进会话文件：pi 把 system 消息写进 transcript，恢复时采用存下来的那一份；atlas 的提示词由启动时的 cwd 与上下文文件现算，所以换分支、改 `AGENTS.md`、换 `-system-prompt-file` 对续跑的会话立刻生效（只在打开 pi 建的会话时采用记录的那条 system 消息）。上下文文件本身读的是同一批名字与同一个顺序，但没有 pi 的 worktree / symlink shadow 检测，也没有 `--append-system-prompt`。
- 没有独立的 `convertToLlm`：会话专有角色在 `internal/session` 投影时转成 user 消息，transcript 里只存 `wire.Message`。`finishTurn`/`prepareRequest` 钩子仍未实现。
- 会话列表是同步串行的（pi 有并发上限与进度回调）；分支导出的标签尾链按 (labelTimestamp, targetId) 排序，pi 按插入顺序；`ForkFrom` 顺带把旧格式条目迁移到当前版本，pi 直接复制。
- 工具参数没有全量 JSON Schema 校验，只有解包、类型解码和错误信息；这是 M1 之前的取舍。
- 工具没有 `executionMode` 单工具覆盖，只有全局 sequential/parallel。
- 思考级别的钳制只对"客户端告诉内核的模型表"生效：RPC 的 `set_thinking_level` 与 CLI 的 `-thinking` 走同一个 `fitThinking`（词表外报错、`off` 恒放行、在轴上原样、其余夹到最近的档位），夹出来的值不随响应返回，客户端从 `get_state` 读；"给不能思考的模型发档位"在 atlas 是错误（`does not think, so only off can be sent`），pi 会静默夹成 `off`。内核没拿到模型表时（`-demo`，或前端自己构造的 `app`），`get_available_thinking_levels` 退回七级词表、`set_thinking_level` 只校验词表、`cycle_model` 与 `cycle_thinking_level` 回 `data: null`。
- 侧会话是 Go 侧新增的能力，pi 只有会写文件的分支。
- SSE 读取器在 EOF 时补发未以空行结束的事件（网关常省略最后一个空行，被丢掉的往往是 `[DONE]`）；裸 CR 不视为换行。读错误是粘滞的，不会退化成 `io.EOF`。
- openai 适配器有两条思考分支：`ThinkingDialect: "deepseek"` 一次发两个字段（`thinking` 开关加 `reasoning_effort`，`off` 只发 `thinking: {type: "disabled"}` 不带 effort），标准方言只发 `reasoning_effort`——`low` / `medium` / `high` 原样，`xhigh` / `max` / `ultra` 与认不出的名字夹到 `high`，`off` 与空级别在这一家的词表里没有对应值，整条省略，让端点自己的默认值生效；标准方言只对声明了 `Reasoning` 的模型下发，`Compat.DisableReasoningEffort` 是关掉它的逃生门。`Compat.ToolChoice` 原样填 `tool_choice`，`SendStore` 发 `store: false`。仍不做：Responses 那套嵌套的 `reasoning.effort` 形状、`chat_template_kwargs`、`strict`（OpenAI 的 strict 要求每个属性都进 `required`，与 anthropic 那套用 `additionalProperties: false` 兜住开放对象的规则不同，所以两边不共用同一份实现）。
- anthropic 适配器有三条请求侧分支，由 `Compat` 挑一条：`ThinkingDialect: "deepseek"` 发 `thinking: {type: "enabled"}`（`off` 是 `disabled`）加 `output_config.effort`；`AdaptiveThinking` 发 `thinking: {type: "adaptive", display: "summarized"}` 加同样的 `output_config.effort`，模型自己决定想多久；两者都不开时才是 budget 式 thinking。effort 在这两条路上都只有 low / medium / high 三档，其余档位落到 high。`StrictTools` 与 `wire.Tool.Strict` 同时成立、且这次请求里的 strict 工具不超过 20 条时才带 `strict: true`：schema 里出现关键字表外的形状、或格式不在白名单里，那一份整份降级为不带 strict，不逐条删关键字——把 `pattern` 悄悄抹掉再盖上 `strict: true`，等于让模型以为约束生效了。`Compat.ToolChoice` 翻成 `auto` / `none` / `any`（`required` 落成 `any`，这家没有 required）。仍不做：`anthropic-beta` 头（strict 与 adaptive 都已转正，不需要它）、内建工具开关、`eager_input_streaming`、OAuth 身份头；`responseId` / `responseModel` / `rawStopReason` 不回填；未知 stop reason 报 `Unhandled stop reason: X` 而不是当成功结束；`max_tokens` 默认 4096、预算不足 1024 时不发 thinking（pi 会兜底到 1024 再让 API 拒绝）。
- responses 适配器有四个可选的请求旋钮，全部由 `Compat` 的字符串给，空值一个都不发（这几家的接受面不一样，默认值留给端点自己）：`ToolChoice` 写 `tool_choice`（`auto` / `none` / `required` 原样，与 chat completions 同一套拼法）、`ServiceTier` 写 `service_tier`、`Verbosity` 写进 `text.verbosity`（与 `format` 同层）、`Truncation` 写 `truncation`；后两个的形状是照 OpenAI 公开的请求字段自补的，pi 只实现了 `service_tier`。仍不做 `parallel_tool_calls`、`max_tool_calls`、内置工具与 `input_file`。它与 chat completions 那家一样是无状态的：`store: false` 恒发、不带 `previous_response_id` / `conversation`，上下文每次整段送回去，reasoning item 靠 `ThinkingSignature` 里那段原始 item 回放。pi 同时支持服务端存上下文，atlas 不做（第 8 节第 71 条）——理由是会话的真相只能有一个，而删除、归档、换 key 都发生在本地。代价是必须带 `include: ["reasoning.encrypted_content"]`，也所以没有 Azure 那条从 `response.completed.output` 回填密文的补丁。
- 工具结果里的图片转成尾随用户消息：chat completions 的工具角色只承载文本，所以连续的若干条工具结果各自发 `role: "tool"`，期间收集到的图片合成一条 `role: "user"`（`Attached image(s) from tool result:`）。模型声明 `Input` 不含 `"image"` 时不发图；`Input` 为空表示未声明，按可收图处理——静默丢图比端点报 400 更难排查。
- 压缩估算不引 tokenizer：非 ASCII 按 UTF-8 字节计（英文比例与 pi 相同，中文约 0.75 token/字，pi 的 UTF-16 长度会低估约 4 倍）；摘要 prompt 里的工具参数按名排序，思考文本不进序列化。
- 切割点遇到被 `context_edit` 清空的条目会停止 metadata 回走：pi 会跨过它，把已摘要的条目当成新 turn 的开头。压缩设置不持久化。
- 手动压缩要求空闲：`App.Compact` 对运行中的会话返回 `ErrBusy`，RPC 的 `compact` 先 abort 再调用（pi 的 `compact()` 自己先 abort）。
- 溢出靠一个结构化错误码判定，不靠调用方读英文：`wire.Message.ErrorCode` 是 Go 侧新增的字段（pi 没有，它全凭正则），四家适配器的每条失败路径最后都走 `provider.Frame.Error`，所以由那里用 `provider.ClassifyText` 打码，适配器一行都不用改。分类只看错误文本，不看 vendor 的 `type` / `code`：适配器本来只保留 `error.message` 的正文，`type` 早就丢了。排除表优先于匹配表（rate limit / too many requests / throttle / deadline exceeded / context canceled），否则 Bedrock 那句 `Too many tokens` 会被当成溢出，对健康分支白压一次再重试一个必然失败的轮次。
- 溢出恢复只有一次机会：先把失败的那条助手消息用 `context_edit` 置为 `null` 藏起来（它还在文件里，只是不进投影），再以 `overflow` 原因压缩，然后用**空 prompt** 重跑——起始那条 prompt 还在上下文里，再追加一次等于把同一个问题问两遍。第二次仍溢出就不再压缩，改发一条可行动的话。`runTurn` 因此收的是切片，nil 或空表示"续跑"。
- 准入校验在发请求之前，且把待发的那条 prompt 算进去（它还没进分支，任何压缩都动不了它）：估算越过阈值但仍在窗口内，先压缩、无论成败都照发——请求还装得下，摘要失败不该拒活；估算越过窗口，先压缩，压不下去就不发，返回 `Context overflow: N estimated tokens do not fit a M-token window.` 这句。这一条同时补上了旧缺口：`Ask` 过去只在 `err == nil` 时才跑 `autoCompact`，被中断的轮次不会在下一轮之前补压缩。`settings.Enabled == false` 时准入与恢复一起关掉——用户已经接管了。
- `compaction_end.willRetry` 现在反映真实情况：只有 overflow 路径可能为 `true`，而且只在调用方真的会重试时。阈值压缩仍然静默（不发 `compaction_start`），manual 与 overflow 提前发。手动压缩与 overflow 在"没有可压的区间"上的处理不同：前者照旧报失败，后者把这条哨兵错误留给调用方，换成上面那句更有用的话——`nothing to compact` 是会话的事实，不是请求失败的原因，不该出现在给读者的句子里。溢出放弃时的句子不引用任何 token 数字：那时分支已经被摘要过，它的估算描述的是重试而不是失败的那次请求。
- `atlas` 是 Go 侧新增的形态：pi 有 `--mode print/json/rpc`、`!` 直通 bash、模型与会话选择器；atlas 只有一个二进制、一个全屏会话、一个 `-demo`。界面在 0.4.0 换成了全屏（alt-screen、自有 transcript、审批弹窗、侧会话视图、0.4.1 起有欢迎页与命令面板，0.4.2 起 `/model` 与 `/resume` 有方向键菜单，0.4.4 起模型那一栏还能用 `←`/`→` 转思考档位、回答改成一条贯穿整块的竖线，0.4.5 起侧视图里 `Ctrl+C` 一步退回、侧会话也有自己的首页、自己打的那句用另一种颜色并两侧留白，0.4.6 起一句话没说的会话不再留下文件、离开也不补写，0.4.7 起输入区与对话之间留一行空白、`/archive` `/recover` `/restore` `/delete` 让归档会话不进 `/resume` 但仍然可读，0.4.8 起连按两下 Esc 或 Ctrl-C 才停一轮、停完把排队的行、屏幕上的模态框与工具进程一起收干净，0.4.9 起打开 atlas 不新建会话、`/archive` 之后直接回到欢迎页并把收据写在页上，0.4.10 起间距、颜色与布局都能由文件调——`colors` 叠在主题上、`layout` 0.4.10 是九个键、0.4.11 起十个（多了输入区与状态栏之间的间距），不写就是 0.4.9 那一套，0.4.11 起这些外观设置搬进自己的 `tui.json`，由 `config.json` 的 `"tui"` 引用，一个设置一个家，0.4.12 起审批框多一个"这个工具本次会话不再问"的答案、状态栏末尾有了带秒数的忙碌标记，0.4.13 起几何按屏幕块命名——`blocks` 管区域、`roles` 管 transcript 内的正文块，扁平的十键被拒绝并逐键指路，0.4.14 起菜单与命令面板共用同一份行排版——`▸`、列宽、滑窗规则只剩一处，两张列表不再各自漂移，0.4.15 起整块屏幕有底色、输入区凸成一圈圆角框（框里的底色由 `background` 推一档得到，不是第十三个 token），0.4.16 起一轮结束的收据还报 `tok/s`、这一轮用了多久与几点几分停的，0.4.17 起 atlas 自己在启动时读 cwd 与每一层祖先目录的 `AGENTS.md` / `CLAUDE.md` 拼进系统提示词、`/init` 写完立刻生效、`-no-context-files` 是唯一的退路，0.12.1 起这份提示词每轮重算，装完 skill 或改完 `AGENTS.md` 下一轮就生效，0.12.3 起 `@` 能把文本与图片带进这一轮、全屏会话里还能补全路径，0.12.4 起回答按 markdown 画——表格、围栏代码块、行内样式、六级标题与列表，按 `(宽度, 配色, 文本)` 缓存所以同一宽度重绘不重复解析，0.12.5 起 `/model` 与 `/resume` 的菜单能打字收窄——只搜标签与说明、每个词都要命中、光标留在原来那一行上，零命中时框里说一句而不是空着，0.12.6 起 `/resume --all` 与 `/recover --all` 能把别的项目的会话真的打开——工具集与系统提示词都按会话记录的目录重建，所以切过去之后 bash 的沙箱、`fs` 的边界与 `AGENTS.md` 一起换成那个项目的，屏幕上多一行 `dir:`，0.12.7 起 `/export` 能写三种格式——扩展名定格式（`.md` / `.html` / `.jsonl`），HTML 单文件自包含而回答里的表格与代码块是真的表格与代码块，JSONL 是一个还能 `-resume` 打开的会话文件），0.12.8 起 bash、MCP 与 `webfetch` 被截断时会把全文落进会话桶的 `spill/` 并回一行路径——只有「再问一次也拿不到」的截断才落盘，`read` / `grep` / `ls` / `find` / `websearch` 仍然只是头部截断加一句怎么变窄，与 pi 同型，0.12.9 起有四个只读的 git 工具——`status` / `diff` / `log` / `show` 各一个，只读不是承诺而是构造：每个 token 过一张按子命令写死的 argv 白名单，表外报错而不是被忽略，`--` 之后一律当路径，而 `-c` / `--textconv` / `--ext-diff` 这类会执行外部程序的选项单独拒绝并各带一句理由；它们不套沙箱，代价是自己补上 `git status` 刷新索引那唯一一次写（`GIT_OPTIONAL_LOCKS=0`，有测试比对 `.git/index` 的字节），颜色也从 `GIT_CONFIG_*` 关而不是从 argv 关，因为 `status` 根本没有颜色旗标；0.12.10 起权限有了四档——`Ask`（缺省，逐条问）、`Auto`（没有任何规则可依的 ask 交给 `permission.classifier` 指定的那个模型判，判不了就回来问人）、`Full access`、`Off`（真的关掉闸门，连 deny 规则都不跑），`/permission` 切换、`/permission status` 查看、状态栏把当前档接在工具摘要后面、headless 走 `-permission`；分类器只看工具名与参数、看不见对话，规则型 ask 与 `bypassImmune` 的 ask 一律不交给它，`bypassPermissions` 仍排在所有 ask 之后；仍缺的是把剪贴板里的图直接粘进输入行（`Ctrl+V`）以及一个 `--file` 开关，忙碌标记也只报秒数、不报这一轮正在做什么。0.12.11 起 MCP 的工具表会在会话中途重列——服务器发了 `tools/list_changed` 就在下一轮生效，每台可以写 `tools` 白名单按工具名收窄，`/mcp` 的 TOOLS 列写 `N of M`、表下分别说明「白名单要了但服务器没给」与「上次重列失败、表是旧的」，而重列失败保留旧表、不把那台标成 failed；0.12.13 起一轮因为超窗口而失败时，纯文本路径上那行不再套 `Compaction failed:` 前缀——它自己已经是完整的一句（多少估算 token 装不进多大的窗口，以及两条出路）。0.12.15 起 transcript 的块按段归并——同一角色相邻的框合成一段，点标题行把它折成一行 `▸ <标题>`、`Ctrl+G` 一次全折再按一次全展，鼠标拖过就按单元格选中并复制到剪贴板（高亮用反显而不是 `selection` 底色，因为反显在只认十六色的终端上也成立），`/fork` 还能挑一条旧消息分叉并把那条正文回填进输入行，状态栏的项目名后面挂上 git 分支（`proj (main)`，读 `.git/HEAD` 而不 spawn `git`，worktree 与 detached 也认）。全屏的代价是终端历史缓冲区里不留这次会话，所以"把对话存下来"由 `/export` 负责。
- 插件只有用户级、没有市场：`$ATLAS_HOME/plugins/<name>/plugin.json` 一个目录声明一组 skill 与几台 MCP 服务器，没有项目级（`<cwd>/.atlas/plugins` 不读）、没有 install / enable / disable、没有版本与签名——停用就是删目录。pi 与 MCode CLI 的插件是带生命周期的包，这里刻意只保留"声明"这一半（`docs/plugins.md`、architecture 第 82 条）。
- MCP 只做"能连、能列、能调"：pi 的 `exposure` / `toolExposure`（逐工具的暴露档位与通配）与 `description`（进系统提示词的服务器列表）都没有，所以暴露粒度只有「整个服务器」与 `tools` 白名单两级，白名单只能「全有」或「只这些」；`!command` 这种整值凭据也没有。按服务器收窄与 `tools/list_changed` 在 0.12.11 补上：后加的工具下一轮就出现，`/mcp` 的 TOOLS 列因此是 `N of M`。`GET` 长连流与 progress 有，全屏会话里也画得出来（`tool_execution_update` 覆盖在调用行的尾巴上，250ms 一次、折成一行截 60 字），只是 `-prompt` 与管道的纯文本渲染没有这一栏——那一路只往后追加，没有可以覆盖的那一格（`--json` 与 `-rpc` 拿到的仍是每一条原始事件）。OAuth 有，但只有一条路：动态注册加授权码加 PKCE 加刷新，客户端认证只有 `none` 与 `client_secret_basic`，step-up、client credentials、CIMD 与设备码流都不做。项目级文件只能开开关：pi 只在项目被信任时读项目级条目，atlas 没有"信任一个仓库"这一层，于是干脆不许它写 argv 或凭据（`auth`、`scopes` 与 `tools` 一并拒）。`ping` 也不做——每一处需要知道连接是否还活着的地方，要么正握着那把锁，要么已经超时。
- 配置是一份扁平 JSON，不是 pi 的 settings + models.json + 环境变量三层。目录接在 CLI 与 RPC 两处（窗口、输出上限、模态与档位轴，配置里写了的优先，规则见 [docs/catalog.md](docs/catalog.md)）：`-list-models`、`/model` 菜单、`-thinking`、`get_available_models`、`cycle_model` 与 `set_thinking_level` 读的是同一个 `factsOf`。配置本身仍是扁平的：没有整份 models.dev 的生成器，"可用模型表"就是配置里声明的那几个（组合根把它填进 `app.Config.Models`），也没有"临时发现一个本机模型"的路。
- 系统提示词的内置部分 `go:embed` 进二进制（`cmd/atlas/system.md`），是精简版：skill 名单与上下文文件由 `promptBuilder` 拼在它后面，不写在这个文件里；模板与按 provider 分段的段落仍然没有。`-system-prompt-file` 整个换掉的是内置部分，skill 名单照旧追加。
- 打开 `-session` 时保持文件里记录的 cwd，不 chdir：工具仍在当前目录执行，而 pi 会把工作目录切到会话记录的那个。
- 凭据在组合根挂到适配器上（`provider.WithAPIKey`），既不进内核也不进会话文件；pi 由 provider 自己读环境变量或 oauth token。
- RPC 只实现内核能诚实服务的命令，其余五个回 `success:false`（`set_auto_retry`、`abort_retry`、`bash`、`abort_bash`、`export_html`），`get_commands` 恒为空数组；`get_state` 的 `autoCompactionEnabled` 与 `isCompacting` 反映进程内真实状态，但压缩开关不持久化；`get_session_stats` 与 `fork` / `clone` / `get_fork_messages` 在 0.12.2 接上（`contextUsage` 紧跟 compaction 且尚无 post-compaction 回答时按 pi 规则发显式 `null`）。
- 系统提示词每轮合成、不入 transcript，所以 `get_messages` 与 `get_state.messageCount` 比 pi 少一条 system 消息；`get_last_assistant_text` 无文本时是 `data:{}` 而非 `{text:""}`。
- 流式中 `set_model` / `cycle_model` / `set_thinking_level` 返回 busy，pi 允许在 turn 中切换以影响下一轮——那需要 `app` 支持中途换 agent。
- 中断之后接着跑是一条命令，pi 没有：`/continue` 读分支尾部的形状（`app.Continuation` 回 `answer` / `tools` / `prompt` / `none`），助手说了半句就补一条 `Continue from where you were interrupted.`，工具被插断、或者用户自己那句话还没被回答就直接跑，这一轮答完了就拒绝。为此两处留了东西，pi 都没有：被打断的助手消息保住已经说出去的正文（pi 那种 `stopReason: "aborted"` 的消息正文是空的），被打断的工具结果保住已经产出的输出并打上 `wire.Message.Interrupted`——用结构化字段而不是回头去匹配那句 `Operation aborted`，因为工具自己报同一句话时字符串上分不出来。侧会话里没有这条命令：它续的是主会话的那一轮，在侧视图里跑会让屏幕描述一段它没在说的对话。
- 目标模式是 codex 与 MCode CLI 都有的能力，atlas 0.12.22 起给的是更小的形态：`/goal <objective>` 设目标并开循环，模型连续工作，自己停下来等输入、读者回答后循环接着跑；出错自动暂停，`/goal resume` 继续、`/goal clear` 清除。状态是会话里 append-only 的 `custom` 条目，重启后还在；token 预算来自 `config.json` 的可选 `"goal": {"tokenBudget": N}`（0 或缺省 = 无预算），设目标那一刻快照，之后改配置不影响已有目标，到达即软停。循环由界面层调度、`internal/app` 的 `Ask` 契约不变，所以只有全屏会话有循环（`-prompt` 与管道没有命令表、天然没有 `/goal`）。与 codex 最大的功能差是没有受限的 `update_goal` 工具与独立 verifier：完成判定是提示词里的一句话，不是模型能自己宣布的（`docs/goal.md`、architecture 第 103 条）。

## 测试

`./tests/run.sh` 逐个模块跑 gofmt、vet、test 与 test -race，任一失败返回非零：一个模块全绿才允许进入下一个模块。`./tests/run.sh <module>` 只跑指定模块。覆盖内容：

- fixture 往返：每条消息解码再编码后与原始 JSON 语义一致
- 字符串内容形态保留、`contentIndex: 0` 保留
- system 消息的 tools/sections 回放
- faux provider 的文本、工具调用、错误、脚本耗尽
- 错误码：`ClassifyText` 十五条，一条一种真实措辞（anthropic 的 `prompt is too long`、413 的 `request exceeds the maximum size` 与 `request_too_large` 类型字段、openai 的 `exceeds the context window`、代理的 `maximum context length`、以文本形式转发的 `context_length_exceeded`、google 的 `input token count … exceeds the maximum`、groq 的 `reduce the length of the messages`、llama.cpp 的 `exceeds the available context size`、z.ai 的 `prompt exceeds max length`，外加四个反例：Bedrock 式的 `Too many tokens` 被 `ThrottlingException` 拦下、普通 rate limit、`context deadline exceeded`、401，以及空正文），`Frame.Error` 只在 `error` 上打码而 `aborted` 不打、不带码的失败保持空码
- 参数解包：字符串包裹、代码围栏、字符串数字
- 循环：纯文本、工具调用、未知工具、错误工具、并行完成顺序、顺序模式、steering、follow-up、before/after 钩子、terminate、length 截断、abort、中断时保住已说出的正文而丢掉 thinking 与 tool call、被打断的工具结果留 `Operation aborted` 与已经产出的输出
- SSE 分帧：OpenAI 文本流与交错的工具调用分片重建为完整消息、Anthropic 事件名与 payload 类型一致、注释/多行 data/CRLF/BOM/超长行等边界
- openai 适配器：请求体 golden 逐字段比对、文本/工具调用/思考流重建、`finish_reason` 缺失与未知值、流内 `error` 对象、HTTP 错误体（四种形状，含空体，每条都断言错误码：溢出那条带 `context_overflow`、其余不带）、abort、不可表达的请求在发出前被拒绝、DeepSeek 方言的思考四态 golden（无级别 / off / low / max 四份整请求体，外加"方言为空时 `thinking` 与 `reasoning_effort` 都不出现"）与六档到三档 effort 的映射全表；0.12.12 补的五条请求侧用例（标准方言的 effort 九档 golden、只对声明 `reasoning` 的模型发、`DisableReasoningEffort` 关掉、`tool_choice` 五个取值、`store: false` 开关）
- anthropic 适配器：请求体 golden 逐字段比对（system 折叠、图片、thinking 签名、`tool_use`、合并的 `tool_result`、`input_schema` 兜底）、文本/思考/工具调用/密文思考流重建、签名分片拼接、`stop_reason` 各值与未知值、缺 `message_stop` 的截断流、非法工具参数、`data` 无 `type` 时回退事件名、路径与 `anthropic-version` / `x-api-key` 头（含覆盖与不写空 key）、HTTP 错误体（四种形状，外加溢出、`exceeds the maximum size` 与"rate limit 不是溢出"三条，每条的码都断言）、abort（等响应头时与流式中）、不可表达的请求在发出前被拒绝、DeepSeek 方言的四态 golden（空级别不发参数、`off` 只发 `thinking: {"type": "disabled"}`、low 与 max 发开关加 `output_config.effort`）、六档到三档的映射全表、方言为空时 `budget_tokens` 原样且 `output_config` 不出现；0.12.12 补的十一条请求侧用例（adaptive 四态 golden、effort 九档、需要 `reasoning` 声明、opt-in、`tool_choice` 五值且需有工具、strict 双声明四组合、对象补 `additionalProperties: false`、六种被拒关键字整份降级、支持子集的嵌套、二十条上限）
- responses 适配器：请求体 golden 逐字段比对（无状态、`developer` 角色、工具声明、`prompt_cache_key`）、`store` 恒为 `false` 且不省略、六种思考级别/模型组合（非 reasoning 模型整块不发、空级别与 `off` 发 `effort: "none"` 且无 `include`、high 带 `summary: "auto"` 与 `encrypted_content`、`ultra` 落成 `max`、未知级别落成 `medium`）、`max_output_tokens` 下限、reasoning item 逐字段原样回放与非 item 签名被丢弃、工具结果四种 output 形状、文本/思考/工具调用/`incomplete`/`failed`/截断流的重建、路径与 `Authorization` 头、流内错误与 401（含溢出带码、401 不带）、取消；0.12.12 补的四条（`tool_choice` 五个取值与需有工具、三个旋钮一起走、三个旋钮都不出现）
- google 适配器：解码十二条（文本、思考与签名、工具调用合成 id、`finishReason` 各值与缺失、截断流、空流、usage 拆分）、请求侧十一条（两类思考方言、`off` 发 `thinkingBudget: 0`、`Compat` 覆盖嗅探、温度与思考并存、工具 schema 用 `parameters`、图片的两种送法、签名三重条件）、六张一致性场景、溢出被识别成 `context_overflow`
- conformance：harness 自己的逐缺陷自测（没有 start、两个终态、终态之后还有事件、丢 delta、参数不同、少一个调用、stop 不对、文本不符、delta 指向未开的块、块跳过索引、消息多一块、终态仍 pending、usage 不符、error 用例却成功、错误消息不符、partial 被改写，以及请求侧九种缺陷），四家适配器各自的一致性场景（终态唯一、delta 之和等于块正文、块编号从 0、请求侧语义、并发两条流结果一致）；请求侧的两个开关 `ModelInPath` / `StreamInPath` 让把 model 与「要流」放 URL 的协议也能过同一套断言，而不是给它开一个跳过检查的例外
- fs：glob 语义（`**` / `{a,b}` / basename 匹配）、gitignore 嵌套优先级、路径解析与 macOS 变体、CRLF/BOM/模糊匹配、图片魔数（含 APNG 拒绝）、diff 往返与超限回退、read 各截断分支、edit 的错误路径与并发串行化、ls/find/grep 的上限与提示语
- shell：stdout/stderr 合流、非零退出、cwd/env/commandPrefix、尾部截断与落盘（落点就是调用方给的那个目录、内容 2500 行一行不少、notice 里那条绝对路径能被 `read` 读回来）、超时、超时杀进程组（用后台子进程验证整组死掉）、abort、partial updates
- 后台任务：`run_in_background` 的十条（带 `Recorder` 时 schema 多一个参数、不带时与 0.12.17 逐字节相同、前台不碰 recorder、`bg_` 编号与「先写行再起进程」的顺序、输出落进文件且读得回来、到点自己杀组、没有 recorder 就拒绝、启动它的 ctx 取消不影响它、外部 `kill -TERM` 报 128+signal、命令首行截到 80 字节且不切碎多字节字符），`internal/task` 的二十八条（终态不可改、`stopping` 收成 `canceled`、有错误收成 `failed`、`Age` 两种口径、末条同 id 胜出、坏行跳过、目录 `0700` 与文件 `0600`、另一个进程的任务读成 `lost` 且文件字节不变、`Read` 的分片与 offset、`Stop` 的三个拒绝、`Close` 只收本进程名下的、`Close` 之后拒绝 `Register`），三个工具的十四条（空表不是错误、只列本会话、单条全貌、状态筛、未知 id、`wait_ms` 等到新输出、没有输出文件的读法、`task_stop` 对一个已经结束的任务不是错误、活性计数与退出码），接线的三条（三个工具进集合、没有服务时一个都不进、记录落在会话目录）
- web：搜索的请求体（query、默认 count 10、`need_content` 不上送、`Authorization` 里有 key）、排序行（score 与 authority 在首行、未知权威度写 `?`）、没有 summary 时退回 snippet、count 钳制（500 → 50、0 → 10）、空 query 与超长 query 一个请求都不发、空结果文案、抓取（`<page url>` 包裹、`file://` 与带 userinfo 的 url 被拒、没有正文时报错并带上标题、被截断时全文落盘而答案是文件开头）、没有 key 就不注册、5xx 重试一次而 4xx 不重试、402 指向余额且整条错误里没有 key、已取消的一轮不发第二次
- git：白名单放行（四条命令各自的常用组合）、`--` 之后的路径不解释成选项、七个会执行外部程序的选项各拒一条并断言理由里说的是那句话（`--textconv` 根本到不了 git）、表外选项报错、不发明 `-sb` 这种捆绑短旗标、`--untracked-files=all` 与 `--unified 3` 两种写法都收而 `noValue` 带等号被拒、颜色从 `GIT_CONFIG_*` 关、环境里继承的 `GIT_CONFIG_COUNT` 被丢掉；真 git 临时仓库里的四条端到端（工作区、`--stat`、`-n 1 --oneline`、`-s --format=%s`）、非零退出是 `IsError` 结果而不是 Go error、截断提示说得出怎么变窄且不出现文件路径、`status` 跑完 `.git/index` 字节不变、没有 git 时零注册、四个工具的名字与参数声明、`cmd/atlas` 把 cwd 原样递给这个家族
- sandbox：profile 的生成（先 `(deny default)`、再按 `DenyRead` 拒读敏感路径并放通目录元数据、再按可写根放通、再逐条拒 `.git/hooks` 与已存在的 `.git/config`，顺序即语义）、Linux `bwrap` 的挂载顺序（`--ro-bind / /`、可写根 `--bind`、`DenyRead` 的 `--tmpfs` 与 `/dev/null`、写保护 `--ro-bind`）、路径解析跟符号链接、没有可写根时拒绝而不是给一份全拒的 profile、扫描找得到嵌套仓库而深度用完就停、hooks 在还不存在时就已经被拒、环境里丢掉全部 `GIT_*` 并补上固定值、`Wrap` 幂等（`ATLAS_SANDBOXED=1` 时原样返回）、没有执行器时报错而不是放行；六条**真的套上 Seatbelt 跑**的内核测试（工作区内写得进、工作区外写不进、写 `.git/hooks` 被拒、改写已存在的 `.git/config` 被拒、`/dev/null` 可写、`DenyRead` 文件读不出但父目录 `ls` 照常，以及 `git init` / `add` / `commit` / `status` 照常——这六条在非 darwin 上跳，而 `tests/sandbox/run.sh` 会把"跳过"判成红）、shell 侧（argv 过 `Wrap`、`Wrap` 报错时命令不执行）、接线（`cmd/atlas` 端到端看到 `ATLAS_SANDBOXED=1`、默认把 `~/.ssh` 等凭证目录接进 `DenyRead`；没有 `sandbox` 块时 `sandboxFor` 返回 nil）
- permission：deny 规则优先于 allow 规则与 `bypassPermissions`、`bypassPermissions` 下无规则放行而 `bypassImmune` 与显式 ask 规则仍然问、`default` 下无检查器放行而有检查器未决则问、内容级 `command` 规则（`:*` 前缀与 `*` 通配，含复合命令管道切分）、`bash` + 沙箱激活 + `autoAllowBashIfSandboxed` 只跳整工具 ask 而不跳内容级 ask 与 deny、沙箱未开时该开关不生效、坏 `permission.json` 在 `default` 全问而在 `bypassPermissions` 全拒、未知 `version` / 未知字段 / 未知 `matcher.kind` 指名报错、主对话 `write` 被 ask 拦下时工具不跑且模型收到可行动拒绝理由、四档各自的阶梯（`off` 在阶梯最前直接放行、连 deny 规则都不跑，`bypassPermissions` 仍在显式 ask 之后）、运行时可换档而未知名字被拒、`auto` 只把"没有规则产生的 ask"交给分类器而规则型与 `bypassImmune` 的不交、分类器 deny 停下调用且 `auto` 越不过 deny 规则、阶梯已经 allow 的调用不发那次请求、五种失败一律回落问人（没有分类器、分类器报错、没有结论、给出的是 `ask`、模式根本不是 `auto`）
- classify：两个判决各一条、问题里只有工具名与参数而没有对话、代码围栏与尾随句都不改判决、八个不可用答案各自报错（散文、`confirm`、缺 `verdict`、空正文、坏 JSON、provider 报错、`length` 截断、回答是一次工具调用）、没有 provider 时一个请求都不发、超时按 `DefaultTimeout` 生效
- openai 工具结果图片：图片转尾随用户消息、并行结果合成一条、纯图片的占位文案、文本模型丢图、`requiresToolResultName` / `requiresAssistantAfterToolResult`
- compact：估算（ASCII / 中文 / 图片占位 / usage 基线在 compaction 后失效）、切割点（保留预算、toolResult 永不可切、split turn、`context_edit` 空洞阻断回走）、摘要（初始与 UPDATE prompt、预算封顶、`error` / `length` / 工具调用 / 空正文四种失败、序列化截断）、`Prepare` / `Run`（增量摘要与文件清单继承、hook compaction 不继承、无历史占位、失败不落盘、空 detail 编码为 `[]`）、`willRetry` 的两种取值（manual 恒 `false`、overflow 重试时 `true` 而放弃时 `false`）
- app：归档锁（归档后落到新会话、空会话拒绝归档、`/restore` 原地恢复且能继续追加、活会话拒绝 `/restore` 与 `/delete`、`/delete` 只对归档生效、十条写操作在归档会话上全部 `ErrArchived` 且文件字节不变）、`message_end` 落盘（含工具结果）、并发 turn 拒绝、模型切换落条目且下次请求生效、思考级别只在变化时落条目并透传、续跑时采用会话的模型与级别、分支重投影、steering 在 turn 中注入并与主请求同批送达、侧会话继承快照且不污染主会话、重复 `BeginSide` 替换旧侧会话、关闭后拒绝提问、侧会话替换主 system prompt、`new_session` 复位（含 turn 进行中拒绝）、手动压缩在 busy 时拒绝、阈值压缩在 turn 结束后触发且失败不失败 turn、关闭开关后不压缩、溢出恢复（失败的那条被 `context_edit` 置 `null`，重开文件后仍进不了投影、三次请求的顺序是失败轮加摘要加重试、第二次溢出不再压而改发一句可行动的话且 `willRetry` 两次为 `true`/`false`、摘要失败时只有一个请求也就是那次摘要）、准入校验（换成阈值压缩所以 prompt 还没入分支、缩不下去时拒绝且一个请求都不发、拒绝的句子里不出现 `nothing to compact`、`Enabled: false` 时连超大 prompt 也照发、被中断的轮次会在下一轮之前补压缩）、`runTurn` 的"续跑"（nil 或空切片发一次请求、不再追加 prompt、产出的消息里只有回答）、fork 续写新文件而旧文件逐字节不变（`parentSession` 指旧文件、名称随分支走、busy 时拒绝、in-memory 会话 fork 后仍不落盘）、`ForkMessages` / `ForkAt` / `CloneSession`（按追加序过滤非空用户文本、第一条消息分叉建空会话并延后落盘、中间条目分叉重采纳分支旋钮并清理队列/侧会话、未落盘与空叶拒 clone）、`SessionStats`（全会话汇总 `usage`/`compaction`/`branch_summary`/`toolResult`/`assistant` 与 `Cost`、无窗口省略 `contextUsage`、紧跟 compaction 且无新回答时置 `nil`）、`switch_session` 换会话文件（恢复模型/级别/系统提示词/名字、失败时原地不动、busy 拒绝、队列与侧会话随旧对话丢弃、缺文件报错而不是新建、别的项目的会话被拒、记录目录已被删的会话被拒）、`Continuation` 的尾部形状（答完了、说了半句、工具被插断、用户自己那句没被回答、失败轮也算没答完、中间批次的工具结果、按 runes 数字符）、`Continue`（半句话补一条固定说法的用户消息并把它算进字符数、工具结果不补、用户消息不补、请求数不变地跑完这一轮、答完了拒绝）、重启之后仍能从同一个断点续
- config：插件目录（`plugin.json` 的两类贡献各一条、没有插件目录时静默、名字不合规的目录跳过、十种坏声明各一条、缺 `skills` 目录只丢一半、一好一坏时好的照旧进会话与提示词、撞名先到者赢、`-`/`_` 近似名只出 note、项目文件能关掉插件服务器、插件是唯一来源时也读得出来）、严格解码（未知字段、trailing data、超大文件、缺文件保留 `os.ErrNotExist`）、校验的每个拒绝分支、`provider/model` 与裸 id 与唯一兜底、歧义报错、`apiKeyEnv` 覆盖与空值回退、`~` 展开、首启模板可被自己加载且绝不覆盖、示例文件合法、`thinkingLevels` 的每个拒绝分支（未知名字、重复、把 `off` 写进列表、非 reasoning 模型声明档位、默认级别不在声明里）与 `Gears`/`AllowsGear` 的顺序表；`mcp.json`（两级文件合并、项目级只能写 `enabled`、`type` 可省与两个传输的推断、`sse` 拒绝、`${NAME}` 展开且未设时记进 `Missing`、只差 `-` / `_` 的两个名字、名字里的空格、头里有换行、相对 `cwd`、非正 `timeout`、把 `headers` 写在 `command` 服务器上、未知字段与未知顶层字段）
- cli：`/plugin` 的五个列（空描述写成 `-`、SKILLS 按插件目录计数、空表说往哪放、坏插件报原因、带参数拒绝、三种会话状态都能跑）、`cmd/atlas` 的审批门（同一会话里同一个工具只问一次、名单只认被问过的那个工具、换会话重新问、一次性的"是"不进名单、"否"不进名单、记住的答案不再打扰读者来问）、一轮的收据（notice 的四段形状、被拒的轮不出声、速率的分母是写字时间而不是整轮、个位数速率留一位小数、没写字就不出速率、`TotalTokens == 0` 时退回 `[stop reason ...]` 一句）、`-demo` 的两种输出、demo 不落盘与 `-session-dir` 落盘、首启建模板后报错、显式 `-config` 不建文件、上下文文件（由外向内、一个目录只取第一个有内容的候选、`AGENTS.override.md` 与 `AGENTS.MD` / `CLAUDE.MD` 的拼写、空文件不藏下一个、同名目录不算指引、读失败报错、全局文件在前、没有文件时提示词逐字不动、`-system-prompt-file` 仍追加、`-no-context-files` 不动）、真实 turn（httptest 回放 SSE：凭据进 `Authorization`、系统提示词与工具声明进请求体、会话文件含双方消息）、anthropic 路由与 `x-api-key`、stdin 当 prompt、空 stdin 报错、`-tui` 在管道上明确报错、`/help` 与命令表逐行对齐、`/quit` 不再是命令而 `/exit` 仍是、flag 冲突与位置参数、纯文本渲染的 golden（turn 形状、无增量回答、并行打断、失败正文、压缩行——含 overflow 那条不加 `Compaction failed:` 前缀而是原样成句、中断轮）、`/model <name>[:level]` 的解析（整串优先、剥离后缀、模型不存在时报 base 的错、级别必须是该模型声明过的）、裸 `/model` 在没有菜单时只打印（末行 `current:`）、菜单选中后换模型、菜单里 `←`/`→` 换档位后取走的是 `m:level`、菜单开在当前级别那一行、没有档位的模型那一行不带轴、取走时不带档位的模型仍走 `m`、关掉菜单什么都不改、状态栏各段（token 格式对齐 pi、`~` 归约、路径缩短、权限归纳、`side` 在最前、没有档位时整段消失、降级阶梯在每个宽度都不超宽、估算只在分支或模型变化后重算、`/model` 之后下一帧就反映、项目名后面挂 git 分支（`proj (main)`、带斜杠的分支名、在子目录里也读得到、detached 报短 sha、worktree 的绝对与相对 `gitdir:`、没有 `.git` 时整个括号不出现、换分支后重读、仓库短暂消失后又找回、分支名带斜杠时页脚不会印出半个名字（1–200 全宽扫描里分支名要么完整要么整段不在、80 列的整行、55 列时目录整段退出、三级目录帧各自的取值、真仓库里整行带着分支名）））、被取消的 turn 只打 `aborted` 摘要而不当错误、中断的 turn 只报一次 `interrupted`（不当错误、不吞掉取消）、`/resume`（裸命令在没有菜单时打印表格、菜单开在当前会话那一行且选中后换会话、关掉菜单原地不动、id 前缀命中与歧义、带空格的路径原样解析、别项目的会话被拒、`--all` 列出别的项目而恢复仍被同一条校验挡住、`-resume` 按路径或前缀开文件且与 `-session`/`-continue` 冲突、记录的目录被删后启动即失败并提示 `-cwd`、给了 `-cwd` 则重开并打 note、恢复后把已记录的消息写回屏上——用户行 / 助手文本 / 工具行 / 失败结果正文 / 错误原因 / 图片 prompt，空会话不打 history，全屏会话那份重放走 `Sink.Answer` 画成带竖条的块）、`/new` 与从空会话切走都不落盘（连续两次 `/new` 之后会话目录还是空的）、换成一模一样的空会话之后落在首页而不是 transcript（`/archive` `/delete` `/new`：翻一次 transcript、报告只留头一句上页、`/new` 一句不说、`/archive` 带上 `/recover`、换到有内容的会话时报告与历史照旧进 transcript）、空会话在页脚里没有会话段（`sessionField` 等第一条记录）、`/fork`（打印新会话 id 与文件、in-memory 时注明、旧会话的消息留在新会话里；0.12.15 起还有选择点：没有菜单时打一张编号表、`/fork 2` 直取并回填输入行、`/fork 0` 是整段对话、菜单里选中哪条算哪条、关掉菜单不动会话、越界与非法号报错且句子里指路 `/fork lists them`、空会话说 `nothing to fork`）、`/rename`（换行折成空格后回读、无名字时报 `(unnamed)`）、`/export`（Markdown 逐字文档：thinking 在前、system 跳过、消息数不含 system、正文里的围栏让外层加长、默认文件名落在 cwd 且两次导出只有一个文件、相对路径、缺父目录时报错——三种格式各跑一遍且都不把目录造出来；扩展名定格式与裸 `--md` / `--html` / `--jsonl` 的默认名、认不出的格式在造文件之前就拒绝；HTML：表格与围栏代码块是 `<table>` / `<pre><code class="language-go">` 而不是源码里的原文、整轮都在、自包含、工具输出被转义、空会话说自己空；JSONL：能被 `session.Open` 当会话文件重新打开并回到同一个投影、每条都指向前一条；渲染器 25 条表驱动断言（标题与 `#hashtag`、围栏与嵌套围栏、未闭合围栏、表格与列对齐、缺表头的分隔行当文本、列表与嵌套列表、有序列表的 `start`、引用、分隔线、行内代码、粗斜体、删除线、`snake_case` 不当强调、`2 * 3 * 4` 保持原样、链接与属性转义、`javascript:` 写成文本、图片写成链接、`<script>` 被转义、反斜杠转义）加两条收口（松散表格补齐、语言名消毒）、`/init`（模型用 write 工具写下的文件被确认并报大小、只回文本不落盘时报 `AGENTS.md was not written`、写完之后系统提示词里就有刚写的内容并注明"读进提示词"、设了 `-no-context-files` 时注明没读）、提示词每轮重算（下一轮读到新写的 `AGENTS.md`、下一轮列出新装的 skill、读失败时这一轮不发出去）、`/reload`（报几个上下文文件、几个 skill、多少字节、改没改、逐个列出读到的文件、会话自带提示词时如实说自己那份没法重载）、侧会话（快照继承、`/side off` 丢弃、`/parent` 保留、`side` 段只在侧视图出现、空心标记、拒绝会挪走会话的命令、门禁：读不问 / 写要问 / `y` 才跑 / 无终端时拒绝 / 主对话不经过它）、`Ctrl+O` 双向切换与没有侧会话时的提示、侧会话的首页取代新开时那条通知（重进仍然打，因为那时没有页了）、换掉侧会话时清空侧视图（`/parent` 与 `/side` 只挪提示、不动它）、`compat.thinkingDialect` 一对一抄进两家适配器的 `Compat`、`thinkingLevels` 收窄可选档位、`-thinking` 在开会话之前失败、目录接线（只写 id 的模型拿到表里的窗口 / 输出上限 / 模态与窄轴、声明值逐项压过表、目录不打开 `reasoning`、不认识的 id 一个字段都不补、`max` / `xhigh` / `ultra` 在 gpt-5 上都夹到 `high` 并带说明、`off` 对任何模型都放行、没有轴与拼错仍是错误、`/model m:level` 的夹取说明、`-list-models` 打印表里的数字）、`/mcp`（没有服务器时指出配置在哪、服务器表按名字排序且失败原因打在表下面、单台的工具表把远端名与模型看到的名字并排、名字不存在时报错而不是猜一个、MCP 工具在页脚归到 `mcp:<server>` 一组）、`@` 引用（`parseAttachRefs`：词首、`@"…"`、`~`、绝对路径、散文里的 `bob@example.com`、括号后的标点、引号没闭合报错；展开：文本内联、图片 inline、带空格的路径两种写法、缺文件与超限都拒绝、散文加的尾部标点只在原样不存在时才剥；候选：只列一层、保留已打的前缀、只认行尾；接线：全屏一轮把图片送进请求、headless 缺附件时一个请求都不发、回放不把附件正文打在提示下面、bash 的输出落在会话桶的 `spill/` 子目录里且 `session.List` 不把那个目录当会话）、`/continue` 的七条（note 说出续的是几个字符、第二次请求里带着断点那几条消息、这一轮答完了拒绝、带参数拒绝、不是侧会话命令、在帮助表里、归档会话里被守卫拒绝）、`/goal` 的十三条（空状态报 `no goal`、没有 turn runner 拒绝且不落盘、设目标跑两轮到模型停且 objective 以用户消息进 transcript、pause 留目标并报恢复路、resume 把 paused 目标放回去干活、active 时 resume 拒绝、clear 后读作没有目标、无选择器时第二个 objective 报错不覆盖、菜单是替换唯一入口且关掉保留旧的、到达预算软停成 budget_limited 且记账 5 of 5、轮次失败自动 pause 且保留目标、重开会话后目标仍在、主对话轮成功后 active 目标循环接着跑并报进度）
- update：版本解析与比较（`atlas-v` 前缀剥离、逐段数字比较、非数字段回落字符串序、坏 tag 报错）、只报比当前新的版本（查不到与已最新都是静默）、`/update` 升级（菜单确认后下载替换、替换后带 0755、收尾打 `updated to atlas X; restart atlas to run it`）、菜单拒绝保住原二进制（`staying on atlas X`）、已是最新只报一行、坏校验和与缺 `SHA256SUMS` 与缺资产各拒绝一次且不碰原二进制、命令表接线（rewriteTransport 把 `api.github.com` 重写到 httptest 服务器，`/update` 从命令表跑到升级）、repo 注入与还原（`setUpdateRepo` 空值保持默认）
- rpc：分帧边界（CRLF、空行、EOF 残余、U+2028 不被当换行）、写者字节精确且不转义 HTML、`message_update` 去 `partial` 并补 `usage`、`toolcall_start` 补 `id`/`toolName`、`get_state` 全字段、prompt 事件流与落盘、流式中 prompt 的三种分支、abort 等 turn 收尾、空会话 `get_messages`/`get_entries` 返回 `[]`、`since` 找不到报错、未实现与未知命令、解析失败无 `id`、写失败与 EOF 停 turn、`compact` 的事件流与响应、无法压缩时的失败文案、压缩先 abort 正在跑的 turn、`set_auto_compaction` 与 `get_state` 的真实开关、模型表（`get_available_models` 列配置里的模型且顺序稳定）、`cycle_model`（带着档位走、不能思考的模型落到 `off`、`isScoped` 恒 `false`、只有一个模型时 `data` 是显式 `null`）、`cycle_thinking_level`（沿轴走、没有轴时 `data` 是显式 `null`）、`set_thinking_level` 的夹取（`max` 在窄轴上落成 `high` 且无 data、给不能思考的模型发档位仍报错、词表外报错）、内核没拿到模型表时退回七级词表、`get_session_stats`（零费用仍保留 `cost: 0`、无窗口省略 `contextUsage`、紧跟 compaction 显式发 `tokens: null`/`percent: null`、工具/助手/compaction 累计）、`get_fork_messages`（空数组与多条目）、`fork`（指定条目分叉并返回 `text`、第一条分叉回空会话、进行中先 abort、错误 `entryId` 报 `Invalid entry ID for forking`）、`clone`（空叶与未落盘拒绝、`cancelled: false`、只往新文件续写）
- session：归档（`/resume` 不再列、`/recover` 能列而文件仍在、归档两次不算错、没有文件时拒绝、`Unarchive` 可重复、`DeleteFile` 连标记一起删、归档不改字节也不改 mtime）、条目往返（未知字段保留、未修改的行按原始字节回放）、坏行跳过与尾换行修复、非会话文件拒绝、v1→v3 与 v2→v3 迁移、compaction 边界与上下文投影、`context_edit`（含 null 删条与 assistant 字符串归一）、懒落盘、分支与 `ResetLeaf`、标签、分支导出（剥标签 + `firstKeptEntryId` 重指）、`ForkFrom`、列表与最近会话查找、按 header cwd 过滤（尾斜杠算同一目录）、跨项目合并（`ListAll`）、无 cwd 的会话在过滤时被跳过、编码相同的两个项目 `-continue` 互不串门
- theme：两个内置调色板都能解析且十二个 token 全非空、`#abc` 展开成 `#aabbcc` 与大小写归一、`default` 与十六进制是两种值、颜色拒绝表（颜色名、无 `#`、位数不对、非十六进制、空串、大写 `DEFAULT`）、未知字段与未知 token 拒绝、缺 `version` 与版本不支持、未知 `mode`、空 `colors`、缺 token 时一次列全并按规范顺序（且不列已有的）、有 `extends` 时可只写要覆盖的、尾随数据与超大文件拒绝、坏文件只登记不拦启动且报错带路径、坏文件与内置主题同名时不让内置顶替（`auto` 也跟着报错）、近目录覆盖远目录、缺目录与非 `.json` 文件与非文件跳过、`extends` 环路、链长上限、链尾主题不完整在加载期就指名那个文件、未知主题名报错并列出可选的、`Broken` 与 `Names` 排序、`Dirs` 的顺序
- catalog：七级词汇表与拒绝表（`HIGH`、`minimal`、带空格的名字都不算命中）、clamp 的四条规则与"等距取更低的一档且与 `offered` 的顺序无关"、`Gears` / `AllowsGear` 的三态（不能思考没有轴、声明为空是全档、否则 `off` 加声明项）、`Lookup` 只认精确 id（`GPT-5` 与 `openai/gpt-5` 都查不到）、`All` 按 provider→id 排序且改不动表里的东西、九条种子数据的每个字段、`Load` 的二十一条拒绝（不是 JSON、未知字段、尾随数据、缺 `source` / `snapshot`、空 `models`、缺 provider 或 id、id 带空白 / 斜杠 / 控制符、窗口为 0、输出为 0 或挤占窗口、未知模态与重复模态、非 reasoning 声明档位、档位含 `off`、重复档位、未知档位、一个裸 id 被两家 provider 声明）与乱序档位加载后按规范序排好、条目不按文件顺序输出
- tui：输入区与 transcript 之间那一行空白（有回答时上面一行不是空的、gap 行是空的、光标行等于 `inputTop`、面板打开时空白在面板之上、整帧仍是整屏）、十二个 token 到 `lipgloss.Style` 的映射（十六进制变成 SGR、`default` 不涂背景）、transcript 折行与工具调用的配对（按 `ToolCallID` 收尾、并行打断先补一行）、流式回答不重复也不丢、标记与空行规则、输入行的折行与宽字符光标、历史保住半行草稿、粘贴里的换行变空格、提交与事件折叠、审批模态框（`y` 是、`n`/Enter/Esc/Backspace 否、第二个问题直接拒、答完整屏重画）、停一轮要两下（一下只武装且输入行写 `esc again to interrupt`、中间按别的键就撤、两种键共用一次武装、turn 自己结束不留武装、Esc 与 Ctrl-C 都能停、审批框吃掉的那一下也算撤且框里的 Esc 只是「不准」）、停完之后要还的（审批框与菜单不留在屏幕上而是回一个空答案、排队的行回到输入行且草稿在后）、滚动夹在两端、两个视图各自的状态、没有宿主时不动视图、turn 中拒绝切视图、宿主输出落进当前视图、一批消息只要一次整屏重画、只在空输入行退出、失败 turn 的提示、`AltScreen`/鼠标/窗口标题/光标这些终端模式、以及"只有一个 reader"（提交时不自建 reader、turn 期间不重复 arm）、菜单模态框（开在当前值那一行、`↑`/`↓` 环绕、`PgUp`/`PgDn` 与 `Home`/`End` 跳、窗口跟随选中且标题报范围、`Esc` 留一个空答案、用不上的键被吞掉而不打进输入行、第二个菜单被拒、`Sink.Pick` 一直阻塞到取走一行）、菜单的搜索（两个形状的列表都能打过滤词、光标留在原来那一行上、命中多于十行时 `End` 之后窗口跟过去且标题写 `3–12 of 12`、零命中时框里写 `nothing matches "…"` 且 `Enter` 不关列表、多词与大小写、`Value` 不参与搜索、`Backspace` 退到查询开头就停住、命中行的 `Value` 与 `Step` 按过滤下标换算回宿主那张表、下一张列表不带上一份查询）、横轴（`←`/`→` 转档位且两端夹取、每行带走自己那一档、没有轴的行把这两个键吞掉且 footer 不提）、回答竖条（每行都有、按宽度折行、重放的答案作为块进 transcript 而不是逐行提示）、`Ctrl+C` 在侧视图里一步退回主对话而主视图仍要两下、turn 跑着时这个键也要两下、用户行整行走 `user` 颜色且两侧各留一个空行、侧视图的首页（按视图号问宿主、字标与版本都不出现、一有块就让位）、首页那行收据（与事实并列而不替换它们、提交一行就作废、不进侧视图的页）、`Enter` 只在"整行是候选名的真前缀"时才补全（0.4.1 会把 `/rename foo` 改写成 `/rename `）、审批框的第三个答案（框里写明工具与会话、`a` 给出"是且不再问"、没有工具名的问句不提供这个键、重绘一次）、忙碌标记（提交后开始转圈、一整轮十帧各不相同并回到第 0 帧、满十帧才出现 `working 1s`、回合结束后既不加也不留、过期的一跳不改变新回合的数、宽度从 80 到 36 到 24 依次丢掉标签再丢掉秒数而帧一直在、窄屏上标记不会被宿主那行挤掉）、两张列表共用的行内核（窗口跟到最后一页后选中行贴在末行、`rows < 1` 兜到 1、选中位置越界时钳制、列宽只量当前窗口、说明列在阈值前后差一格、说明为空与列太窄都不留半列、标签按列宽截断、两张列表的空闲行各用自己那个槽、菜单每行同宽、轴在 35 列还在 34 列就撤、面板行正好一屏宽、空闲行的轴是 Dim 且不带书名号）、长任务的进度尾巴（覆盖同一行而不加行、250ms 内不重画、结果落地让出那一格、没有对应调用行或调用已结束时丢弃、多行与超长折成一行、并行调用按 `ToolCallID` 落行）、`@` 候选就地补全（`Tab` 只替换行尾那段、`Enter` 填了不发、文件来源先于命令面板被问）、回答按 markdown 画（表格、围栏代码块与行内样式各一条、四级宽度下每行都带竖条且不超宽、同一宽度重绘不重复解析而换宽度或换配色会重解析、纯散文与旧的折行路径逐字节相同、非回答角色不被当成 markdown、六级标题保留井号、半截流式输入不报错也不超宽、`Reset` 之后重新解析、每行末尾没有补白、段折叠（点标题行折成一行 `▸ <标题>` 而点正文不折、只在展开时占的行区间上生效、`Ctrl+G` 从全展一次全折再按一次全展、半折状态下按键仍是「全折」而不是交替、`frames off` 时这个键什么都不做、折叠行的标记只借 `border` 且不新编摘要、默认帧与 0.12.14 逐字节相同）、选择（反显只盖选中格且保留原色、宽字形不被切成两半、两个方向都成立、中间整行与中间空行、去补白但保缩进、越界收口、点击不选、跳过不存在的行）、鼠标（单击标题折叠、拖选复制并报 `copied N chars`、一个字也报 `copied 1 char`、下一次按键清掉回执、`Esc` 清选择、首页点击不选、拖出窗口上下边界夹紧））
- mcp：握手（四个版本里任一都被接受并采用服务器回的那个、列表外报错并带上版本号）、stdio 分帧（按 LF 切、容忍行尾 `\r`、跳过空行、残片报 `incomplete JSON-RPC message`）、`tools/list` 跟游标（两页拼成一页、页数上限报错）、名字归一（非 `[A-Za-z0-9_]` 折成 `_`、超长加 8 位 hash、仍撞再加 `_`）、结果映射（text / image / audio 与 blob 落临时文件 / resource 文本头部加正文 / `resource_link` 一行 / 超大文本中部截断并把全文写进会话桶的 `spill/`（没有 Spill 时仍说出一个临时文件名） / `isError` 与 `structuredContent` 原样落位）、工具调用不重试而 HTTP 会话过期重握手一次、连不上的服务器只警告一次且带上 stderr 尾巴、工具声明原样转（描述与 `inputSchema` 逐字段）、Streamable HTTP 会期（`initialize` 拿 session、`Accept` 两边都报、通知回 `202`、`DELETE` 收尾）、`401` 同时给出 `headers` 与 `auth: "oauth"` 两条路、stdio 杀整个进程组（`npx` 起的孙子一起死）、假服务器是测试二进制重入（`TestMain` 认环境变量，不碰真实端点）、OAuth（PRM 的三个候选路径都要试到而 4xx 与 502 只算"这里没有"、issuer 不符时连注册请求都不发、同一个 client 只注册一次、PKCE 只做 S256 且 `plain` 报错、回调 state 不符回 400 且不投递、并发 401 只刷新一次、`oauth.json` 是 0600 且半截文件当空表、声明 oauth 而没 token 时零请求且 `needs login`、`WWW-Authenticate` 的引号与非引号与非法 scheme、`invalid_client` 只重注册一次）、长任务（五条 tick 的调用在 120ms 的空闲预算下跑完、沉默 80ms 后放弃且那句话里带 `silent for 80ms` 与 `timeout`、进度从 `POST` 内联与 `GET` 两条通道各来一次都要接住、`GET` 回 405 的服务器照常调工具、`GET` 上不认识的通知只丢不停读、文案优先用服务器给的那句话、没有对应调用的进度被丢弃、会话中途加的工具在下一轮出现、`tools/list_changed` 比 hook 先到时补一次重列、重列失败保留旧表而不是把那台标成 failed、`tools` 白名单按工具名收窄且项目级不许写、`/mcp` 的 `N of M` 与「缺失」「过期」两行）
- skill：发现（两层深度就停、根里散放的 `.md` 也算、点目录与 `node_modules` 跳过、符号链接跟着进去、目录名大小写不同的 `SKILL.md` 也认）、同名时先到的那个赢且后到的出声、frontmatter 子集的十四行与拒绝的八条（未知键连缩进块跳过、第二个 `---` 属于正文、锚点与流式集合带行号）、`name` 的每个拒绝分支、长描述截断、`allowed-tools` 与 `disallowed-tools` 各出一声、字节与行数超限只警告、空集合整段不出现、目录块的形状与两个转义；`cmd/atlas` 侧（提示词跟随 cwd、共享根被读到、`-no-skills` 逐字节不动提示词、坏 skill 只出声而会话照跑、两个开关互不影响、四个根的顺序、`/skill` 裸命令的表与空表时的四个根、带名字注入的请求形状与 `args` 追加、名字不存在时连被跳过文件的警告一起报、`/skill:release` 的提示、侧会话里正文落在那一边、归档会话里列表照跑而加载被拒、超限拒绝而不是截断）
- deps：三个第三方模块（`bubbletea`、`glamour`、`lipgloss`）按精确版本钉住且不接受伪版本、除 `internal/tui` 与 `cmd/atlas` 外没有包（含测试导入）引用第三方模块、三十个内核包的依赖闭包第三方为空（能抓到经由我们自己的包间接引入的情况）

## 下一步

1. RPC 剩下的五个命令：`export_html`（HTML 渲染 0.12.7 有了，但落在 `cmd/atlas`，`rpc` 够不着：要么把它搬进内核叶子包，要么在 `rpc` 里再写一份——后者不做）、`set_auto_retry` / `abort_retry`（内核还没有重试器）、`bash` / `abort_bash`（`!` 直通在 CLI 侧也还没有）；`get_session_stats` 与 `fork` / `clone` / `get_fork_messages` 已在 0.12.2 做完。
2. 界面侧能力：鼠标拖选复制、点标题折叠那一段（`Ctrl+G` 全折全展）与 `/fork` 的选择点已在 0.12.15 做完（第 9.3 节的 A15）；剩下 transcript 的虚拟滚动（现在每帧把整段按宽度重折一遍，几千行会开始吃 CPU）与 `Alt+M` 那样一键轮转档位的快捷键（`/permission` 已有，快捷键还没有）
3. CLI 侧能力：状态栏的 git 分支已在 0.12.15 做完（读 `.git/HEAD`，worktree、子目录与 detached 都认，落成 pi 那种 `proj (branch)`）；还没做的是 `!` 直通 bash 与删除前的会话预览。图片附件已在 0.12.3 做完（`@` 引用与补全），三种格式的导出已在 0.12.7 做完，跨项目会话的写入与跨项目归档已在 0.12.6 做完，剩下 `Ctrl+V` 粘贴剪贴板里的图与一个 `--file` 开关
4. 本地能力的另一半：只读的 `git` 工具已在 0.12.9 做完（argv 白名单，`diff` / `log` / `show` / `status`，且拒绝 `--textconv` / `--ext-diff` / `-c` 这类会执行外部命令或写文件的选项）；大输出的去处已在 0.12.8 做完。另一边仍是空的：`blame` / `branch` / `remote` 今天只能走 `bash`，也就是需要审批的那条路
5. MCP 剩下的几块：`tools/list_changed` 与按服务器收窄已在 0.12.11 做完（工具表在会话中途重列，`tools` 白名单按工具名筛，`/mcp` 的 TOOLS 列变成 `N of M`）；仍未做的是 OAuth 的边角（step-up、client credentials、CIMD，还有 `/mcp login` 那次等待今天取消不了）、pi 的 `toolExposure` 那种逐工具档位与 `description`，以及 `outputSchema` 没有落到 `wire.Tool` 上
6. skill 剩下的两条：`read` 分页（正文超限时 `/skill` 只能拒绝），以及 `/skill` 加载之后模型会不会一直「当自己是那个 skill」，那要实测。提示词那一半已经在 0.12.1 做完：`app.Config` 收的是函数而不是字符串，提示词每轮重算，`/reload` 显式重算并报读到了什么

7. 子代理（subagent）：这是任务模型唯一被预留却没有实现的那一格。`task.Kind` 里已经有 `KindSubagent`，缺的是生产它的东西——一个能被告知任务、能被 `task_stop` 打断、回来交一份结果而不是往 transcript 里写字的孩子，以及随之而来的三件事：它的权限从哪儿继承（今天的审批门是宿主问人，孩子问谁）、它的 token 记在谁的账上（`SessionStats` 只看得见主会话）、它的输出怎么落到 `task.Task` 那一行上（前台转后台的软让位也没有）。设计见 [docs/architecture.md](docs/architecture.md) 第 9.3 节末尾。

这一节的优先级、依赖与完成标准写在 [docs/architecture.md](docs/architecture.md) 第 9 节末尾的「施工顺序」（十七项工作项，按 P0–P3 排，完成标准要求每条落成一条可跑的测试或闸门断言）；那十七项到 0.12.18 全部走完，所以上面这七条是走完之后还剩的东西，不再有依赖顺序——挑一条做就行。与 MiniMax Code CLI 相比还缺的产品层能力，以及本阶段明确不做的部分（插件市场、Mini App、产品运行时层的其余），写在同一节的 9.1 与 9.2。
