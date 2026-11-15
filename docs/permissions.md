# 权限：四档模式与判定阶梯

这份文档有两半。前六节是动手前的依据：MiniMax Code 0.7.6 的权限系统长什么样、能抄到什么粒度、哪一半依赖它自己的云端因此抄不了。第七节是第一版的范围与验收，第八节是未解问题，第九节记落地时改了什么——0.8.0 的四处（9.1–9.4，第四处是侧会话那道门），0.12.10 把 `auto` 做回来之后的五处（9.5 是四档与分类器）。

它和 [`sandbox.md`](sandbox.md) 是同一件事的两层：沙箱管"被包裹的进程能碰什么"（内核执行），权限管"这个工具调用要不要先问人"（我们自己执行）。`sandbox.md` 第 2 节把这两层叫 L1 与 L2，第 6 节记了那个交换（沙箱开着就跳过 bash 的 ask），但只记到"有这么一条"；完整的那一套在这里。

参考实现是这台机器上的两个产品：

- MiniMax Code 0.7.6（`@minimax-ai/code`）：权限引擎在 `chunks/chunk-53I2XNVN.js`，分类器提示词在 `assets/prompts/llm-gate-classifier.md`（133 行）。**本文的主体。**
- Codex 0.163.0：只有原生二进制、没有源码，能读的只有字符串与内嵌提示词。用来对照。

标注约定：**【mcode】** 从 MiniMax Code 的打包产物与提示词原文读到的；**【codex】** 从 Codex 的二进制字符串读到的；**【atlas】** 我们自己的决定。前两个都是"读到的"，不是"实测的"——没有接口能把它们的权限引擎单独叫起来跑一遍，所以读不出来的地方都标出来（第 3 节末尾）。

## 1. 三个问题，不是三个模式

主动询问 / 智能授权 / 始终授权听起来是三个模式，实际是三个问题的答案：

| 问题 | 谁回答 | MiniMax Code 把它放在哪 |
|---|---|---|
| 被包裹的进程能碰什么 | 内核（Seatbelt / bwrap） | 独立开关 `permission.sandboxEnabled`，与模式无关 |
| 什么时候问人 | 规则表 + 各工具的检查器 | `mode` 翻成 `askPolicy` 四值 |
| 谁来答 | 人 / 分类器 / 规则 | `askPolicy` 决定 |

**【mcode】** 三档只改第三个问题的入口，不碰第一个。所以"始终授权"不等于"关掉沙箱"，"主动询问"也不等于"沙箱一定开着"——两件事在配置里是两个键。

**【codex】** Codex 把三档做成三元组，一个 preset 同时定三件事：Read Only = `read-only` + `on-request` + `user`；Approve for me = `workspace-write` + `on-request` + `auto_review`；Full Access = `danger-full-access` + `never`。三个 preset 的名字在二进制里是硬的（`read-only` / `auto` / `full-access`），中间那个靠 `approvals_reviewer` 与另外两个分开。

**【atlas】** atlas 今天三个问题一个都没答：沙箱只有一个 `network` 开关，`bash` 在主对话里从不问，只有侧会话有一条按工具名的允许名单（`cmd/atlas/gate.go`）。所以第一版不是"加一个模式选择器"，是先把"什么时候问"这一轴做出来——没有 ask，三档里有两档没有落点。

## 2. 三档是五值枚举上的一层筛选

**【mcode】** 枚举有五个值，界面只列三个：

```js
Rjr = [
  {value:"default",           label:"主动询问 / Ask for approval"},
  {value:"auto",              label:"智能授权 / Smart authorization"},
  {value:"bypassPermissions", label:"始终授权 / Full access"},
  {value:"acceptEdits",       label:"接受编辑 / Accept Edits"},
  {value:"off",               label:"关闭权限检查 / Disable Permission Checks"},
]
kUi = new Set(["default","auto","bypassPermissions"])   // 界面上只列这三个
```

`acceptEdits` 与 `off` 不是删剩的死值：`acceptEdits` 在启动时往全局规则里种三条 allow（`edit` / `write` / `apply_patch`），`off` 在转成别的格式时落成 `bypassPermissions`。它们是"内部模式"，不是"用户选项"。

界面上的选择器就是 `Rjr` 按 `kUi` 过滤之后的三行，也没有专门的键盘循环键位——`shift+tab` 在它那里切的是思考级别。所以"三档"是一个筛选过的枚举，不是一个模式状态机的三个状态。

模式不直接进引擎，先翻成 `askPolicy`：

```js
function sat(e) {                      // e 是 mode
  switch (e) {
    case "bypassPermissions": case "off": return "never";
    case "auto":              return "on-request-llm";
    case "dontAsk":           return "deny";
    case "default": case "acceptEdits":   return "on-request";
    default:                              return "on-request";
  }
}
```

**【mcode】** 这套词汇借自 Claude Code 那六个（`default` / `acceptEdits` / `plan` / `dontAsk` / `bypassPermissions`，再加自己的 `auto`），所以翻译是多向的：转成 Hook 事件的格式时，`off` 落成 `bypassPermissions`，`CODEX` 来源没有 `auto`、落成 `default`，不认识的落成 `default`；规则里的工具名同样要按来源翻译（`apply_patch` 同时匹配 `Write` / `Edit`，`spawn_agent` 同时匹配 `Agent`）。

**【atlas】** 值得抄的是这层翻译，不是那六个词。界面三个选项、引擎四个策略、磁盘上可能还有别的客户端留下的字符串——三处各不相同，中间必须有一层把"用户选了什么"与"引擎该怎么做"分开。少了它，加一档就要改引擎，而引擎是唯一不该为界面改动的地方。

## 3. 判定阶梯

**【mcode】** `checkPermissions` 是一串提前返回，每步都有一条 `Step Nx` 的 debug 日志与一条 `permission.<owner>.decision` 遥测。顺序照抄如下；中间那列是日志里的标签，不是编号顺序（`1d` 在代码里排在 `1b` 之前）：

| 序 | 日志 | 条件 | 结果 |
|---|---|---|---|
| 1 | `1a` | 整工具的 deny 规则 | deny |
| 2 | `1a` | MCP 服务器级 deny 规则 | deny |
| 3 | — | 工具检查器（checker）返回 deny | deny |
| 4 | `1b` | 整工具的 ask 规则，**但 `bash` + 沙箱开着 + `autoAllowBashIfSandboxed` 时跳过** | ask |
| 5 | `1b` | MCP 服务器级 ask 规则 | ask |
| 6 | — | 检查器抛错 | ask |
| 7 | `1e` | 需要交互的工具 | ask |
| 8 | `1f` | 检查器返回"连 bypass 也不放"的 ask（`bypassImmune`） | ask |
| 9 | `1f` | 内容级 ask 规则 | ask |
| 10 | `1g` / `1h` | 检查器的普通 ask，且当前不是 bypass | ask |
| 11 | `2a` | **模式是 `bypassPermissions`** | allow |
| 12 | `2b` / `2` | 整工具 allow 规则、MCP allow 规则、检查器返回 allow | allow |
| 13 | `3` | 装了检查器但它没表态 | ask（`noMatchingPermissionRule`） |
| 14 | `3` | 没有检查器 | allow |

第 11 步的位置是整套设计的关键：**"始终授权"排在所有 ask 之后，不是排在它们之前**。于是

- deny 规则在"始终授权"下仍然拒绝；
- 写成 ask 的整工具规则在"始终授权"下仍然问人；
- 检查器标了 `bypassImmune` 的 ask 仍然问人。

真正被"始终授权"跳过的只有第 10 步那类"检查器自己觉得该问、却说不出硬理由"的 ask。换句话说 `bypassPermissions` 不是"关掉权限系统"，是"把软判断换成默认放行"。

**【mcode】** `bypassImmune` 有两个来源，都在检查器里：

- 文件路径检查器：敏感路径（系统密钥、凭据文件、`.env`、git 元数据）的 ask 标 `classifierApprovable: true`，于是 `bypassImmune = true`——"始终授权"也不放行。私有运行时数据（`~/.minimax` 那一类）反过来标 `classifierApprovable: false`，于是带 `skipAutoClassifier: true`（分类器不许代答），且 `bypassImmune = false`——"始终授权"能放行，"智能授权"必须问人。
- bash 检查器：`bypassImmune = (ask && 必须人答) || (mode === "bypassPermissions" && ask)`。第二个析取项的意思是"如果我已经判断这条命令该问，而模式会把它吞掉，就把它标成免疫"，否则检查器的判断会被自己的模式悄悄覆盖。

**【mcode】** 两个默认值都在阶梯尾部，方向相反：**装了检查器但没表态 → 问；没装检查器 → 放行**。前者是"这个工具有风险，但我拿不准"，后者是"这个工具没有专属检查器"（`echo` 一类）。抄的时候要保住这个不对称：把所有工具都注册成检查器、让它们默认回 ask，会让每个新工具一上来就要人点同意。

**【mcode】** 规则库读不出来时（`LocalPermissionStoreUnhealthyError`）：非 bypass 模式全部 ask，bypass 模式直接 **deny**——"始终授权"遇到一个坏掉的规则库不是放行，是停手。

读不出来的三处，写在这里而不是猜：

1. 还有一处兜底 `m2r()` 会把剩下的 ask 在 bypass 下改成 allow，理由字符串是 `Allowed under bypassPermissions ("始终允许"): per-call review was waived by the user.`。它和 `bypassImmune` 谁先谁后要看跨文件的调用点，打包产物里读不出确定答案——也就是说"始终授权下哪些 ask 真的会问到人"，引擎给的是 ask，上层可能再改成 allow。这条不抄。
2. bash 检查器接受第四个参数 `"auto"`，让软信号回 `undecided` 而不是 `ask`；但整个文件只有三个调用点，传的都是 `"default"` 或 `"bypass"`。这条通道今天没人走。
3. 分类器在云端，服务端那一半不可见（第 5 节）。

## 4. 规则表

**【mcode】** `permission.json`，`version: 2`，三个数组：

```json
{
  "version": 2,
  "allow": [{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "curl:*"}}],
  "deny": [],
  "ask": []
}
```

- 位置：`$dataDir/permission.json`，另有按 agent 的 `$dataDir/agents/<name>/permission.json`。作用域三个：`global` / `agent` / `session`，会话级的由"这次会话都允许"写进去。
- 两种 matcher：不带 `matcher` 的是**整工具**规则，带 `{kind:"command", pattern}` 的是**内容级**规则。
- 规则排在模式之前：阶梯里先问"有没有规则"，再问"模式说什么"。模式是"没有规则匹配"时的兜底。

**【mcode】** 规则不只来自文件：插件 Hook 也能加规则（`pluginHookPermissionStore`），另有一层"硬检查"（`localHardCheck`）能在 allow 之上把决定升级成 ask——除非命中一条显式 allow 规则。这是第三个来源，第一版不抄。

**【atlas】** 第一版只读 `$ATLAS_HOME/permission.json`，只做 `global` 一个作用域，两种 matcher 都做，内容级只做命令前缀与 `*` 通配。字段名照抄（`version` / `allow` / `deny` / `ask` / `tool_name` / `matcher`），理由同 [`mcp.md`](mcp.md) 第 7 节：用户手上已经有一份，能直接搬是最低的迁移成本。项目级不做——`<cwd>/.atlas/permission.json` 等于让仓库决定 agent 能干什么，而 atlas 没有"项目被信任"那套（同 `mcp.md` 第 7 节对 `mcpServers` 的判断）。

## 5. auto 在阶梯之外

**【mcode】** `auto` 不改变第 3 节那张表里的任何一步，它在表的出口（`resolveEngineDecision`，局部变量换成了可读的名字）：

```js
// askPolicy 是 sat(mode) 给的四个值之一，baseAsk 是阶梯算出来的 ask
return askPolicy === "never"      ? baseAsk                 // bypass：免疫的 ask 原样问
     : askPolicy === "deny"       ? { behavior:"deny", reason:`Denied by dontAsk permission mode: ${baseAsk.reason}` }
     : askPolicy === "on-request" ? baseAsk                 // 默认：问
     : this.classifyDecision({ … });                        // auto：交给分类器
```

也就是说引擎先算出"按规则与检查器，这次调用该 allow / ask / deny"，只有结果是 ask 时 auto 才有事做。allow 与 deny 都不经过分类器。

分类器不在本地：

```js
async classifyViaCloud(t) {
  return this.cloudGateway.classify({ …, mode:"auto", conversationContext: t.conversationContext })
}
```

**【mcode】** `classifyDecision` 入口有两道闸：`skipAutoClassifier`（策略说这次不许代答）与 `tat()`——后者是 `isManagedRuntime()`，实现是"有没有托管的 access token"。没有它就 `skipped / unavailable`，直接问人。**所以 auto 只在用厂商托管服务时才是智能授权；BYOK 或离线时它退化成主动询问**，而且这个退化是静默的（只有一条 info 日志）。

请求带上工具名、被分类的那段文本（bash 取 `command`，文件工具取 `file_path` / `path` / `pattern`，其余 `JSON.stringify`）、`platform`、`home_dir`、`workspace_root`、最近用户消息与最近对话、agent 名、会话 id。超时 60 秒（`classifierTimeoutMs` 缺省 `6e4`）。失败一律按 ask：抛错走 `vZ(decision,"ask", timeout)`，`mapCloudDecision` 里 `block` 与 `confirm` 都落成 ask，只有 `allow` 落成 allow。

**【mcode】** 提示词（`assets/prompts/llm-gate-classifier.md`，可被 `$dataDir/internal/prompts/llm-gate-classifier.md` 覆盖，需开 `beta.promptOverride`）的骨架：

- 强制调 `classify` 工具，四个字段 `{verdict: allow|confirm|block, reason, is_irreversible_delete, confidence}`；**跳过工具调用按 `confirm` 处理**（第 7 行原文：`Skipping the tool call is treated as fail-closed "confirm"`）。
- 输入全部当不可信数据（intent / context / conversation / action text 都可能带注入），工具名与参数才是事实。
- `block` 只留给不可逆的破坏：擦除、格式化、破坏数据集/卷/快照、截断文件、归档后再删源码。可恢复的删除（废纸篓）不是 block。
- 授权与工作流顺序分开：用户说"放心干""全权授权"能让默认 confirm 的操作变成 allow；用户说"先做 X 再做 Y""顺序反了"不是安全信号，不许据此降级。第 124 到 131 行专讲这一条，还举了一例：用户上一句是"合入"，那是对眼前这个 merge 的具体授权，不该当成工作流噪声再问一遍。
- 授权不能越过 block patterns：`curl -T ~/.ssh/id_ed25519` 在"全权授权"下仍然是 block。
- 一处细节值得记：`rm` 改写成 `mavis-trash` 发生在 **default 模式的 bash 检查器**里，不在这个 gate 里。所以 auto 模式按命令原文判，看到 `rm` 就是不可恢复的删除（第 33 行原文：`In auto mode (this gate) judge the command as written`）。同一件事在两个模式下判据不同，这不是 bug，是"改写发生在检查器、分类器只看到原文"的直接后果。

**【atlas】** 云端那一半抄不了，也不该抄：把权限判断发给第三方服务，等于把"我要执行什么"连同最近的对话一起交出去。0.8.0 因此不做 auto（第 7 节）。真要做只有一条路——用配置里的模型自己判，而那会带来一个新问题：**同一个模型既提议又批准**，它的判断会被自己的上下文污染，这正是 Codex 用第二个模型的理由。

0.12.10 走了这条路，三个前提各自有一个答案（第 9.5 节）：不发往第三方，因为分类器用的是 `config.json` 里用户自己写的模型；"既提议又批准"用一条独立键 `permission.classifier` 解，它可以指向另一个模型，且不随 `/model` 换掉；"看不见对话"是这个实现的取舍而不是遗漏——请求里只有工具名、参数与阶梯说明，所以模型自己写进对话的那句话没有投票权（第 8.4 节）。

## 6. 失败语义与两级的成本

两边都做了"两级"：先用便宜的手段过滤，剩下少数交给贵的手段。

| | 便宜的一级 | 贵的一级 | 失败时 |
|---|---|---|---|
| MiniMax Code | 规则表 + 各工具检查器 | 云端分类器（60 秒） | ask |
| Codex | 异步分类器（单 token `high` / `low` + `review_threshold`） | Guardian 子代理（读完整上下文再判一次） | 拒绝，并告诉模型不要绕过 |

**【codex】** Codex 的失败语义写得更明确：`This is a review failure, not a determination that the action is unsafe.`——审查挂了不等于"这个操作危险"，但结果一样是不执行。它还记着"最近一次自动审查被拒"，并允许 `approve one retry`，因为一次误判不该让用户重新组织语言。

**【atlas】** 两条要抄的是语义，不是实现：

1. **失败即询问。** 分类器超时、报错、回了个读不懂的东西，都当 ask。任何"失败就放行"的路径都是拿安全换可用性，而那条路径只在出事那天才被发现。
2. **拒绝要说清"这不是判决"。** 模型收到一句"被拒了"会去猜原因，然后换个写法绕；收到"审查失败，请让用户决定"会停下来问。

## 7. 【atlas】第一版的范围

做：

1. `internal/permission`：模式、规则表、第 3 节那张阶梯。stdlib-only 叶子，只依赖 `tool`。
2. 三档里做两档：`default`（问）与 `bypassPermissions`（不问，但 deny 规则与 ask 规则照旧）。`auto` 的配置键不写——写下一个永远退化成 ask 的 `"auto"` 比没有这个值更差（同 `mcp.md` 第 5 节对 `exposure` 的判断）。0.12.10 补上了 `auto` 与它的配置键 `permission.classifier`：那句话的前提是"没有云端就没有分类器"，而换成配置里的模型之后前提不成立（第 9.5 节）。
3. `$ATLAS_HOME/permission.json`，`version: 2`，字段名照抄（第 4 节）。只 `global` 作用域。
4. 接线在已有的缝上：`app.Config.BeforeToolCall`（`internal/app/app.go:84`，返回 `agent.Block`），今天只有侧会话用它（`cmd/atlas/side.go:144`）。第一版让主对话也走这条路，侧会话那条允许名单折进规则表。
5. `autoAllowBashIfSandboxed` 按第 3 节第 4 步的粒度做：只跳过"整工具的 bash ask 规则"，不跳过内容级 ask、不跳过 deny，且要求沙箱真的开着。
6. 会话级的"这次会话都允许"只进内存，不写文件——写文件就是第 4 节那个 `session` 作用域，而它需要先有"哪次会话"的持久身份。

不做：

1. ~~`auto` 与 LLM gate（第 5 节）。没有云端，也不该把权限判断发出去。~~ 0.12.10 做了：用配置里的模型，不发往第三方（第 9.5 节）。
2. `dontAsk`（ask 变 deny）。它解决的是"无人值守"，而 atlas 今天只有人守着。
3. 插件 Hook 的规则来源与 `localHardCheck`（第 4 节末尾）。
4. 项目级规则文件（第 4 节）。
5. 遥测与 `/permissions` 命令。
6. Windows 的 `rm` → 废纸篓改写（`sandbox.md` 第 7 节那套在 Windows 上是另一条路径）。

验收（每条一个测试名，`tests/permission/run.sh` 跑它们）：

1. deny 规则优先于 allow 规则，也优先于 `bypassPermissions`。→ `TestDenyBeatsEverything`
2. `bypassPermissions` 下没有规则匹配时放行。→ `TestBypassAllowsUnmatched`
3. `bypassPermissions` 下整工具 ask 规则仍然问。→ `TestBypassKeepsAskRules`
4. `default` 下没有检查器时放行，有检查器但不表态时问。→ `TestDefaultFallsBack`
5. 内容级 ask 规则命中一条命令，同工具的另一条命令不受影响。→ `TestContentRuleMatchesOneCommand`
6. `bash` + 沙箱开着 + `autoAllowBashIfSandboxed`：整工具 ask 被跳过，内容级 ask 不被跳过。→ `TestSandboxSkipsOnlyWholeToolAsk`
7. 沙箱没开时那条跳过不生效。→ `TestSandboxSwitchRequiresASandbox`
8. 规则文件读不出来：`default` 全问，`bypassPermissions` 全拒。→ `TestBrokenRuleStoreFailsClosed`
9. 未知 `version` / 未知字段 / 未知 `matcher.kind` 报错并指到键。→ `TestConfigRefuses`
10. 主对话的一次 `write` 被 ask 规则拦下时，工具不执行，且模型收到一句可行动的话。→ `TestAskStopsTheCall`

## 8. 未解问题

1. ~~**auto 用哪个模型。**~~ 已解（0.12.10）：`permission.classifier` 是一条独立键，写 `provider/model`，空值时落成这次会话启动用的模型。它不跟着 `/model` 走——审批者跟着被审者一起换，正是这条键存在的反义。它是否该默认指向另一个模型（而不是默认落成主模型）仍是一个体验问题：多一条必填键会让第一次开 auto 的人多跳一步，少一条则默认值恰好是最有利益冲突的那个搭配。
2. **规则文件的跨客户端兼容只是形状兼容。** MiniMax 的 `pattern` 是命令前缀加 `*` 通配（`curl:*`），Claude Code 是 `Bash(npm run test:*)`，Codex 又是另一套。照抄字段名能让文件被读进来，但同一份规则在两边匹配到的东西不一定一样——真正要兼容的是 matcher 的语义，而三家没有共同的语义。
3. **`bypassImmune` 在 atlas 有没有对应物。** 它保护的是"系统密钥 / 凭据 / `.env` / git 元数据"这类路径与"已经判定该问"的命令。atlas 的沙箱已经把 `.git/hooks`、`.git/config`、shell rc 挡在写之外（`sandbox.md` 第 7 节），读却完全没隔离（`sandbox.md` 第 9 节第 5 条）。在读有清单之前，`bypassImmune` 保护的是同一批路径的写，价值有限；等清单有了，它就是那个清单的执行点。
4. **提示词的注入面。** 分类器读"最近用户消息 + 最近对话"，而那段对话里有一半是模型自己写的，模型可以往里塞一句"用户已全权授权"来影响判断。提示词第 4 到 7 行要求把这些当不可信数据，但那是提示词层面的约束，不是机制。真正的机制是"分类器只看工具名与参数"，代价是它失去判断意图的能力。
5. **`dontAsk` 与归档会话的关系。** atlas 有归档会话（`app.ErrArchived`），一个不该被写的地方。`dontAsk` 是"问也不问，直接拒"，正好是归档会话该有的模式；但它同时会拒掉读，而读一个归档会话是合理的。要么按工具分，要么不做。

## 9. 落地时改了什么

0.8.0 把第 7 节的范围落进 `internal/permission`（`permission.go`、`store.go`）与 `cmd/atlas`（`build.go`、`side.go`、`gate.go`、`tui.go`、`main.go`）。写代码时有四处具体化或调整：

1. **没写 `"permission"` 块时兜底是 `bypassPermissions`，写了空块 `"permission": {}` 时是 `"default"`。** 第 7 节只说做 `default` 与 `bypassPermissions` 两档，没说 `config.json` 省掉整块时取哪一个。如果省掉整块也取 `default`，所有没写 `permission` 块的既有配置和单测在主对话里一调 `write` / `bash` 就会撞上审批框（或因无终端被拒）；而因为 `bypassPermissions` 在阶梯里排在所有 `deny` 规则与 `ask` 规则之后（第 3 节第 11 步），省掉整块取 `bypassPermissions` 既保住了既有配置的免打扰，又让 `$ATLAS_HOME/permission.json` 里的任何 `deny` 或 `ask` 规则在主对话里立即生效。显式写 `"permission": {}` 的人是在开启审批，所以 `Permission.EffectiveMode()` 把空 `mode` 落成 `"default"`。
2. **`autoAllowBashIfSandboxed` 同时管第 4 步与第 13 步。** 第 3 节只写了第 4 步跳过"整工具的 bash ask 规则"。落地时发现：在 `default` 模式下，就算 `permission.json` 里没有写 `{"tool_name": "bash"}` 这条显式 ask 规则，`bash` 也会因为装了默认检查器（返回 `Undecided`）而在第 13 步落成 `Ask`。如果只跳第 4 步不跳第 13 步，用户还得先去 `permission.json` 写一条 ask 规则才能让它被跳过。所以 `bashAutoAllowed` 同时免掉第 4 步的整工具 ask 规则和第 13 步的未决检查器 ask，而内容级 ask（第 9 步）和 deny（第 1–3 步）都排在它前面，一并保住。
3. **`internal/permission` 的闸门用 `guard_deps_allowed` 而不是 `guard_stdlib_only`。** `tests/lib.sh` 的 `guard_stdlib_only` 连 `pi-go/*` 内部包也一并禁止，而 `Checker` 与 `Engine.Decide` 的入参是 `tool.Call`。所以 `tests/permission/run.sh` 用 `guard_deps_allowed ./internal/permission pi-go/internal/tool pi-go/internal/wire` 加 `guard_no_imports` 禁掉 `agent` / `config` / `sandbox` / `provider`，第三方依赖仍为零。
4. **侧会话的只读门与主对话的权限阶梯合在同一个 `beforeToolCall` 里。** 侧会话先问 `engine.Decide(call)` 是否为 `Deny`（deny 规则在侧会话里同样直接拒，连问都不问），再走 `sideReadOnly` 白名单与 `approve`；用户在任一视图按 `a`（"本次会话都允许"）时同时记进 `allowedTools` 与 `engine.AllowForSession`，两边共用同一个会话 ID 边界。

### 9.5 四档与分类器（0.12.10）

第 7 节"不做 auto"和第 8.1 节那道题的答案在同一次改动里落地：`internal/permission` 收成四档，新增 `internal/classify` 实现 `Classifier`，`cmd/atlas` 把它造好递进 `permission.Options`。五处具体化或调整：

1. **`Off` 是第四档，而且是"真的关掉"。** 它在阶梯最前面直接 `Allow`——deny 规则、检查器、ask 一个都不跑。这是它与 `bypassPermissions` 唯一能区分开的读法：`bypassPermissions` 仍排在所有 deny 与 ask 之后（第 3 节那张表的第 9 步），跳过的只是"没有规则命中时变更类工具默认要问"那一步。两档若只差一个名字，就没有理由把它写进枚举。
2. **`auto` 不改阶梯，只改出口。** `Decision.Classifiable` 只在两处置真：普通检查器 ask（第 8 步）与"没有任何规则命中"的兜底 ask（第 11 步）。规则型 ask 是用户自己写的那句话，bypass-immune 的 ask 连 Full access 都不放行——两者都不可分类，所以分类器永远越不过规则，也永远看不到一条已经被 deny 的调用。
3. **分类器看不见对话。** 请求里只有工具名、参数与阶梯说明。第 8.4 节列的那个注入面因此被结构性地关掉而不是靠提示词约束：对话里有一半是模型自己写的，模型不能对自己能不能动手投票。代价是它听不见"你可以放手做"，于是"用户已经全权授权"这类话不再是它可以用的信号——而这正好符合第 5 节记的那条"工作流顺序不是安全信号"。
4. **失败一律问人，且只认两个答案。** 没有分类器、模式不是 `auto`、provider 报错、超时（30 秒）、`StopLength`、模型调了工具、回了一段散文、`verdict` 是 `confirm` 或空——全部返回 error，`Gate` 随后交给 `approve`。"第三个答案是还是问人吧"不成立：那就是分类器被叫起来之前的状态，让它回一个 `ask` 只是一个空操作穿了件判决的衣服。
5. **侧会话的只读边界不随档位移动。** 主对话与侧会话共用同一个 `Engine`，所以 `SetMode` 一处生效两处，deny 规则与 `Off` 都会传到侧会话；但"读免问、改要问"是侧会话自己的边界，与"谁来回答 ask"是两个问题。`/permission` 在 `Full access` 与 `Off` 下会多打印一句说明，因为在这两档下最容易误以为切换没生效。这条是有意的：侧会话存在的理由是"在旁边问一句而不动主对话的现场"，让它随档位获得写权限会把那个理由撤销掉。

接线的四处：`config.json` 的 `permission.mode` 收四个值（`PermissionModes` 是唯一顺序来源，错误信息按它列），新增 `permission.classifier` 键（必须写成 `provider/model`，空值落成启动时的模型）；`-permission` 开关给 headless 用（覆盖配置里的档位，脚本没有 `/permission` 也没有审批框）；`/permission` 命令给终端用（`status` 报当前档、裸命令画菜单或打印四行）；状态栏那一段从 `toolSummary` 变成 `fs+bash · Auto`，每帧现读，所以切换下一帧就可见。名字解析不到、或适配器建不起来时，`classifierFor` 返回 nil 并给一句警告，`auto` 随后退化成"问人"——这是 fail-closed 的那一侧，退化只多花一次按键。配置里整个 `permission` 块都没有时仍然造分类器：`-permission auto` 是人明确要了 auto，而"没写这条键"与"写了空值"该是同一个答案（落成启动时的模型）；在这里返回 nil 会让那个开关变成一句什么都不做的废话，而那正是这套代码反复要避免的那种失败。模式不是 `auto` 时返回 nil 且不警告——它本来就不会问分类器，那不是失败。
