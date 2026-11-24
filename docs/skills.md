# Skills：抄什么、不抄什么

这份文档回答两个问题：别的 agent 的 skill 是怎么实现的，atlas 该怎么接上。前四节是依据（skill 到底是什么、别家怎么做的、共同的骨架、atlas 的约束），第五到九节是第一版的设计，第十节是每一条拒绝与告警，第十一节是明确不做的事，第十二节是验收，第十三节是未解问题，第十四节是落地记录。

**v1 分两批，两批都已落地**：第一批是 `internal/skill`（发现、frontmatter 子集、提示词里的目录）加 `cmd/atlas` 的接线与 `-no-skills`，第二批是 `/skill` 命令（第 8 节后半、第 12 节第 10 至 13 条）与 `tests/skills/run.sh` 那道闸门。第十四节记着两批落地时各自改掉的地方：先是发现的十处，再是 `/skill` 的六处。第五到七、十节写的是动手之前定下来的规则，落地之后仍成立的部分没有改写。

标注约定：**【规范】** 从 Agent Skills 规范读到的；**【Claude Code】**、**【API】** 从 Anthropic 的两份文档读到的（Claude Code 那一页要读 `.md` 变体才拿得到正文，见第 2.2 节第一句）；**【pi】** 从 `packages/coding-agent/src/core/skills.ts` 与 `packages/coding-agent/docs/skills.md` 读到的；**【本机】** 是这台机器上正在跑的那个 agent 的实测，是一手证据而不是文档；**【atlas】** 我们自己的决定。

## 1. 为什么 skill 不是"更长的系统提示词"

atlas 已经有 `AGENTS.md` 了（`cmd/atlas/context.go:74` 的 `contextFiles`，从用户全局文件一路读到 cwd），它和 skill 都是"给模型看的文字"。那为什么要第二套？

三个差别，每个都能写出反例：

1. **加载时机。** `AGENTS.md` 与会话绑定：每次开始都无条件进提示词，一个 500 行的文件在每一次请求里都占 500 行。skill 只在任务匹配时进。前者是常数成本，后者是可选成本。
2. **归属。** `AGENTS.md` 描述"这个仓库怎么工作"，属于项目；skill 描述"这一类任务怎么做"，属于人。同一个人的十几个项目可以共用一个 `release` skill，一份 `AGENTS.md` 不能。
3. **元数据常驻、正文按需。** `AGENTS.md` 没有元数据层——你要么全读，要么不读。skill 有三层（第 2 节），第一层只有名字与描述。

第三条是形式，前两条是原因。但真正决定怎么接的是下一句：

**skill 不授予任何新能力。** 它是文字。`SKILL.md` 里写"跑 `pytest -q`"，模型还是得调 `bash`；写"读 `references/api.md`"，模型还是得调 `read`。所以 atlas 现有的审批门与沙箱**原样生效**，skill 不需要也不该有一条自己的执行路径。这里没有先例可抄，只有一句话可以检验：一个 skill 能做的事，等于用户手打同样的话能做的事。

这句话把要回答的问题收窄了。不是"怎么执行 skill"，而是三个：

- 怎么发现它（第 5 节）
- 怎么让模型知道它存在（第 7 节）
- 怎么让模型读到它（第 8 节）

三个问题里，第一个要写一个解析器，第三个只是接线（`read` 已经在），第二个是把一段文字拼进一个已经每轮重算的提示词。这也解释了为什么整份设计里最能约束 skill 的东西不是权限，而是"模型看不看得见它"。

## 2. 别家的实现

### 2.1 【规范】Agent Skills

一个 skill 是一个目录，里面有 `SKILL.md`；文件是 YAML frontmatter 加 Markdown 正文，`---` 起止。

- `name` 必填，1–64 字符，只许 `a-z0-9-`，不以 `-` 开头或结尾、没有连续 `--`，**且必须与父目录同名**。
- `description` 必填，1–1024 字符，要写清"做什么"与"什么时候用"——它是模型唯一的挑选依据。
- 可选字段：`license`、`compatibility`（1–500）、`metadata`（string→string 映射，扩展位）、`allowed-tools`（空格分隔，**规范自己标注为实验性**）。
- 可选目录：`scripts/`、`references/`、`assets/`。正文引用它们的路径相对 skill 根，且建议只有一层深。
- 渐进披露三层：元数据（约 100 token，常驻）→ 正文（建议 <5000 token 或 <500 行，按需）→ 资源（按需）。

三层各有预算，这是规范里唯一带数字的部分，也是它唯一在讲"为什么"的部分：模型不能把整个 skill 集合当成一次输入。

### 2.2 【Claude Code】

这一节读的是 `code.claude.com/docs/en/skills.md`——HTML 版抓下来只有页内示例代码，加 `.md` 才是原文。上一版这几条是搜索摘要拼的，下面按原文重写。

**位置有七处**：企业（managed settings 目录）、个人 `~/.claude/skills/`、项目 `.claude/skills/`、子目录嵌套 `<subdir>/.claude/skills/`、`--add-dir` 加的目录、plugin 的 `<plugin>/skills/`、以及从 claude.ai 账号同步下来的（落在 `~/.claude/skills/synced/`）。项目级从启动目录**一路向上到仓库根**；启动目录**以下**的嵌套 `.claude/skills/` 不在此刻加载，要等 Claude 第一次读或改那个子目录里的文件才进来——在那之前 `/` 菜单里没有它，也没法按名字调。

**frontmatter 全部可选**，官方那句是 "All fields are optional"，`description` 只是"建议"。`name` 不给就用目录名；`description` 不给就用正文的第一条非空行。字段名必须一字不差，**不认识的字段静默忽略**，不报错。布尔值除 `true` / `false` 外还接受 `yes` / `no` / `on` / `off` / `1` / `0`，不分大小写。

字段分两类，这个分法对 atlas 有用：

- 规范那六个——`name`、`description`、`license`、`compatibility`、`metadata`、`allowed-tools`——Claude Code 一个不少地接受；而把 skill 上传到 claude.ai、走 Skills API、或用 `package_skill.py` 打包时**只许这六个**，多一个就硬报错，报错原文会列出允许的名字。2.1 节那份六字段清单在这页被印证了一次。
- 其余是 Claude Code 自己的扩展：`when_to_use`（追加到描述后面，一起算进 1536 字符的截断）、`argument-hint`、`arguments`、`disable-model-invocation`、`user-invocable`、`disallowed-tools`、`model`、`effort`、`context: fork` 加 `agent` 与 `background`、`hooks`、`paths`（glob，只在碰匹配的文件时才自动加载）、`shell`。

**`allowed-tools` 不是"声明着玩"的。** 它让列出的工具在**调用这个 skill 的那一轮**免审批，发下一条消息就失效；`disallowed-tools` 反向，把工具从池子里拿掉，同样到下一轮为止。范围是一轮、且不越过权限设置给的底线——这一条让第 6 节与第 9 节的反对理由不能停在"不安全"上。

**坏 frontmatter 有三种表现**，都不致命：开头的 `---` 不在文件第一行时，整个文件（连同 `---`）都当正文；YAML 解不开时 skill 照样加载，只是**一个字段都没有**（`/name` 还能用，自动匹配就废了）；名字里有零宽空格、控制字符、私有区字符、双向控制符、`<` `>`、或空格以外的空白时，Claude Code 拒收这个名字——目录名被拒是整个 skill 不加载，`name` 被拒只是那个名字不能用。

**名字从哪来**：目录名或 `name`，两个都能调。`name` 与目录名不同时，`name` 是 `/` 菜单里显示的那个——`deploy-staging/` 加 `name: deploy` 得到 `/deploy`，`/deploy-staging` 也仍然可用。同名时企业 > 个人 > 项目；嵌套同名不冲突，各带一段路径前缀（`/apps/web:deploy`）。

**自定义命令并进 skill 了**：`.claude/commands/deploy.md` 与 `.claude/skills/deploy/SKILL.md` 都产生 `/deploy`。旧格式还留着，但 skill 多出来的三样东西才是分开的理由——支持目录、frontmatter 能控制谁能调、Claude 能自动加载。所以"skill 由模型挑、slash command 由人打"这句旧说法已经不成立：两者是同一个 skill，差别只在 `disable-model-invocation`（关掉模型那一半）与 `user-invocable: false`（关掉人那一半）。

**成本是明说的**：名字与描述进上下文，正文按需（与规范的渐进披露一致，这页只说"用到才加载"，没有分开讲三层）；列表预算按模型上下文窗口的 1% 算，超了从**最不常用**的 skill 开始丢描述——名字一定还在，丢的是模型据以匹配的关键词。另有一处细节值得抄的教训：`description` 与 `when_to_use` 拼起来超过 1536 字符会被截，所以"最关键的用例写在最前面"不是文风建议。

### 2.3 【API】Anthropic 的服务端形态

- 请求里带 `container.skills`（每项是 `skill_id` + `version`），配 `code_execution` 工具，平台把 skill 目录挂进容器，元数据在启动时加载（第一层）。
- 与本地客户端是两套机制：那边 skill 是被挂载的文件系统内容，执行发生在容器的代码执行器里；这边 skill 是本地文件，执行发生在模型手上的工具里。

对 atlas 的意义：只有一半可抄——"元数据常驻 + 正文按需"的披露方式跟我们一样；容器那半我们没有容器，也不该为了 skill 去有一个。

### 2.4 【pi】本仓库的 TypeScript 实现

`packages/coding-agent/src/core/skills.ts`（514 行）加 `docs/skills.md`。

- **发现根**：`<agentDir>/skills`（用户级）、`<cwd>/.pi/skills`（项目级）、显式 `skillPaths`；另外读共享位置 `~/.agents/skills`（用户级，恒可信）与 `.agents/skills`（项目级，从 cwd 向上走到仓库根，而且要过 project trust——`trust-manager.ts:181`）。
- **发现规则**：目录含 `SKILL.md` 即 skill 根，**不再下探**；根目录下的散 `.md` 也算（本仓库的 `.pi/skills/*.md` 就是这种）；递归找 `SKILL.md`；走 `.gitignore` / `.ignore` / `.fdignore`；跳过点目录与 `node_modules`。
- **校验**：`name` 长度与字符集、`description` 必填且 ≤1024；**不校验 name 是否等于目录名**（规范要求这一条，pi 放掉了）。告警不致命。
- **同名冲突**：保留先发现的（用户级在前），另出一条 collision 诊断。
- **提示词注入**：`<available_skills><skill><name/><description/><location/></skill>…</available_skills>`，并指示"任务匹配 description 时用 `read` 工具读这个文件""相对路径相对 skill 目录解析"。
- **手动调用**：`disable-model-invocation: true` 从提示词里藏起来，只能 `/skill:name` 调；`/skill:name args` 把 args 追加成一条用户请求。
- frontmatter 用 `yaml` npm 包解析（`utils/frontmatter.ts`，40 行）。

### 2.5 【本机】这台机器上的 agent

- 提示词里也有一份 `<available_skills>`，每行是路径 + 名字 + 描述；加载走一个专门的 `skill` 工具。plugin 来的 skill 带 `plugin:skill` 前缀。

这一条的价值不在它做了什么新东西（它和 pi 的形状基本一致），而在于它是**最紧的实现**：连一个把工具集压到很小的 agent 都得让元数据常驻，否则模型根本不知道有东西可读。

## 3. 本地客户端的共同骨架

Claude Code、pi 与本机都是本地客户端，规范是它们共同认的那份目录约定。三家实现加那份约定有三个共同点、一条分歧。API 那家是服务端形态（2.3），扫描与加载在它那里都不存在，只有披露方式可比，所以不在这一节里。

1. **发现靠目录约定，不靠注册表。** 规范只定义"一个目录里放 `SKILL.md`"，扫哪几个目录由实现自己挑；但四家都没有 manifest、没有索引文件、没有安装步骤。理由是 skill 要能被 git 管：clone 一个仓库就得到它的 skill。API 那家没有这一条，因为它的 skill 不是一个目录。
2. **元数据进提示词，正文不进。** 名字与描述是常驻的那一层。四家差别只在格式（XML 标签 / 纯文本列表 / 容器元数据），"只放元数据"没有例外。
3. **正文靠模型自己读。** pi 提示模型用现成的 `read`，Claude Code 与本机各有一个专门的工具（`Skill` / `skill`）把正文放进上下文；两者的差别只是"谁去取这个文件"，四种做法都是把文件内容读进上下文，没有一种是"执行 skill"。这正是第 1 节那句话的实现形态。
4. **分歧：名字与目录是否必须一致。** 规范要求一致；Claude Code 明确允许不一致，还让两个名字都能调；pi 干脆不校验。一致的好处是"看到目录名就知道叫什么"，坏处是重命名目录会静默让一个本来好的 skill 变得不合规。atlas 选第三条路：接受，但出声（第 6 节）。

## 4. atlas 的四条约束

每条都指向一个具体决定，不是背景说明。

1. **内核不许第三方。** `tests/deps/run.sh` 查的是依赖闭包，`internal/` 下除 `tui` 之外不许引第三方。pi 用 `yaml` npm 包解析 frontmatter；atlas 只能手写一个子集解析器（第 6 节）。这不是品味问题——闸门会变红。
2. **提示词启动时算一次，`/init` 之后算第二次。** `promptBuilder.build` 把 base、上下文文件与 skill 名单拼成一段字符串交给 `app.Config.SystemPrompt`，之后每轮都用同一份。所以 skill 名单**不该缓存**这个结论成立，但理由不是"每轮重算"——原稿写的是每轮重算，落地时发现是每轮复用（第 14 节第二点）。代价是会话跑着时装的新 skill 要重开才看得见，写在第七点。**0.12.1 起这条已经改掉**：提示词每轮重算，`app.Config.SystemPrompt` 收的是函数，第 14 节第三批记着那次改动。
3. **`read` 能读工作区外的绝对路径。** `resolveToCwd`（`internal/tool/fs/path.go:16`）对绝对路径原样、对 `~` 展开。所以"用户级 skill 在 `$ATLAS_HOME/skills`"在工具层**不需要任何改动**——这是"用现有 `read` 加载"能成立的前提，也是整个设计里最省事的一处。
4. **`read` 有上限。** `DefaultMaxLines = 2000`、`DefaultMaxBytes = 50 * 1024`（`internal/tool/truncate.go:13`）。超过就被静默截断，而**被截断的指令比被拒绝的 skill 更糟**：模型以为自己读到了全文，然后按半份指令干活，而且它会照着这半份指令把活干完。这一条决定了启动时要量一遍 `SKILL.md` 的大小并告警（第 10 节）。

## 5. 目录与发现

v1 四个根：

| 根 | 作用域 | 理由 |
|---|---|---|
| `$ATLAS_HOME/skills` | 用户级 | 和 `AGENTS.md`、`config.json` 住一起；`ATLAS_HOME` 本来就能改，测试可以指到临时目录 |
| `~/.agents/skills` | 共享 | pi 读这个位置（`trust-manager.ts:188`，项目侧 `.agents/skills` 同源），抄它让一个 skill 不必为每个 agent 复制一遍 |
| `<cwd>/.atlas/skills` | 项目级 | 跟着仓库走，进 git |
| `<cwd>/.agents/skills` | 项目级共享 | 同上，给不装 `.atlas/` 的仓库用 |

**不做祖先上溯。** pi 与 Claude Code 都会从 cwd 走到仓库根，形状和 `contextFiles`（`cmd/atlas/context.go:74`）一样。不抄，理由和 `mcp.json` 只读 cwd 一样：菜单随子目录变化，模型看到的 skill 集在 `cd` 之后就不一样了，而"为什么在 `internal/tool` 下看得见三个、在仓库根看得见五个"没有读者能自己推出来。Claude Code 在启动目录**以下**还留了一手：嵌套的 `.claude/skills/` 要等它第一次碰那个子目录的文件才加载，在那之前 `/` 菜单里没有（第 2.2 节）——这是同一个现象的另一面，它用"菜单会自己长出来"换，atlas 宁愿菜单一开始就固定。`AGENTS.md` 上溯是对的——它是"这个仓库的规矩"，越靠上越泛化；skill 是"这一类任务怎么做"，跟目录深浅没关系。

**两种形态都认。** 目录含 `SKILL.md` 就是一个 skill（不再下探），根目录下的散 `.md` 也认（本仓库 `.pi/skills/*.md` 就是这么放的）。后者不是宽容，是抄一个一手事实：一个人维护三个 skill 时不会为每个建一个目录。

**不读 `.gitignore`。** pi 读，因为它递归扫仓库。v1 只扫四个固定目录、深度两层，撞上生成目录的概率远小于"为它引一个 gitignore 解析器"的成本。跳过点目录与 `node_modules` 这两条照抄——它们不需要解析器，一个名字判断就够。

发现顺序决定冲突时的胜者（第 10 节）：用户级 → 共享 → 项目级 → 项目共享。

## 6. frontmatter 的子集

必须手写（第 4 节第 1 条）。范围划在"够读一份 `SKILL.md`"。

认：

- 顶层 `key: value` 单行标量，值两头的成对引号（`'` / `"`）去掉。
- 顶层 `key: |` 与 `key: >` 块标量（`|` 保行、`>` 折行），也接受 `|-`、`|+`、`>-`、`>+`。Skill 描述会统一去掉首尾空白。
- `#` 开头的整行注释，以及行尾 ` #…`（引号内的不算）。
- 不认识的键**连同它下面的缩进块一起跳过**。这正是规范里 `metadata` 的位置：它是 string→string 的映射，写出来必然是一个缩进块，而扩展位的意思就是"读不懂也要能读下去"。

不认，遇到就报错并带行号，不猜：

- 锚点与别名（`&` / `*`）、流式集合（`[...]` / `{...}`）。
- 认识的四个键（`name` / `description` / `allowed-tools` / `disallowed-tools`）的单行值后出现缩进续行。多行值需要在字段行上显式写出块标记。
- 一个缩进行上面没有任何键：这份文件的结构不是这里读的那种。

报错那句话要说清是什么，而不只是"parse error"：`这是 YAML 的一个子集；把 description 写成单行或 | 块标量`。用户看得懂就能自己修，看不懂就会去查一个我们不支持的规范。

为什么不上一个真 YAML 库：内核不许第三方（第 4 节）。为什么不用正则一把抓：`description` 里出现 `:` 是常态（`Use this when: the build fails`），正则切第一行就会把描述切成半个句子，而描述是模型唯一的挑选依据——切坏了等于这个 skill 不存在。相比"切坏"，报错是更便宜的结果。

字段怎么用：

- `name`：校验字符集与长度（`^[a-z0-9-]{1,64}$`、无首尾连字符、无连续 `--`）。`/skill <name>` 要按名字调，所以名字必须无歧义。
- `description`：必填，缺了整个 skill 不加载。不是"加载了但没有描述"——一个没有描述的 skill 在提示词里是一行空话，模型永远挑不到它，而它还占着位置。
- `license` / `compatibility` / `metadata`：忽略。没有消费者。
- `allowed-tools`：**忽略，并且出声。** 它在规范里是实验性的，但 Claude Code 已经落地成"调用这个 skill 的那一轮免审批、下一条消息收回"（第 2.2 节），所以不能说它是空话。atlas 不认它的理由换了一条：审批门在这里是**会话级**的工具名白名单（`cmd/atlas/gate.go`，一次会话里答应过的工具不再问），"只对一轮有效"是另一种作用域，而它要解决的麻烦（同一条命令反复确认）白名单已经解决了。声明了仍然打一行 note——不说的话，作者会以为自己已经批准过什么。`disallowed-tools` 也一样不认，而且这一条更值得出声：作者写它是为了让某个工具**消失**，忽略的结果是那个工具照旧在。

`name` 与目录名不一致时：接受，出 note。规范要求一致，但拒绝的代价是"用户重命名目录之后 skill 静默消失"，而 note 说出来之后读者能自己改。

## 7. 提示词里的目录

形状照 pi，接在 `promptBuilder.build`（`cmd/atlas/prompt.go`）那两段之后——base 之后、项目上下文之后。最具体的放最后，这是那两段已有的理由，skill 目录同理。

```
<available_skills>
<skill>
<name>release</name>
<description>Cut a release: bump versions, run the gates, tag. Use when asked to release.</description>
<location>/Users/me/.atlas/skills/release/SKILL.md</location>
</skill>
</available_skills>
```

三句话必须跟在目录后面：

1. "当任务匹配某个 skill 的 description 时，先读它的 `location` 再动手。" 不给这句，模型会把描述当正文用——描述写得再好也只是"什么时候用"，不是"怎么做"。
2. "skill 文件里的相对路径相对它所在目录解析。" 不说清的话 `references/api.md` 相对 cwd，于是要么读到别的东西要么读不到。
3. 目录为空时**整段不出现**。一个空的 `<available_skills/>` 是一句关于功能的话，不是关于任务的话，而提示词里的每个字都在占预算。

`-no-skills` 开关与 `-no-context-files` 对称：关掉之后提示词逐字节等于今天。这不是为了灵活，是让"skill 有没有污染默认提示词"这件事可测——`TestAProjectWithoutContextFilesGetsThePlainPrompt`（`cmd/atlas/context_test.go`）已经是同型的断言：没有上下文文件的目录拿到的提示词与 `builtinPrompt` 逐字节相同。`-no-skills` 要的就是这一条。

桌面 Skill 开关按名称保存到 `$ATLAS_HOME/capabilities.json`。停用后，该 Skill 从后续系统提示词的目录中移除，`/skill` 拒绝调用，设置仍可预览和重新启用。

## 8. 模型怎么加载

两条路，都走现成的东西。

**一、模型自己 `read`。** 目录里给的是绝对 `location`，`resolveToCwd` 认绝对路径，`read` 直接能读。零新工具。这是 pi 的做法，也是第 1 节那句话的直接后果：加载一个 skill 不是一种能力，是一次文件读取。

**二、`/skill <name> [args]` 强制加载。** 理由是 description 由模型挑，而模型会挑漏。用户说"用 release 那个"、模型没挑，用户得有办法直接指定。`args` 追加到正文之后，作为这次请求的一部分。

- 命令表第 19 条（`cmd/atlas/repl.go:147` 的 `var commands`）。`/skill` 裸命令列目录（名字、scope、描述收成一行、路径），带名字则加载——与 `/mcp` 同一个手势：裸命令是表，带参数是动作。用户在那里学过一遍，不该在第二个命令上学第二遍。
- 语法是空格分词（`/skill release patch`），不是 pi 的 `/skill:name`。atlas 的命令全部按空格切（`repl.go` 的 `fields[1:]`），为一种命令改分词规则会让 `/model`、`/resume` 全部要重新验一遍，换来的只是少打一个空格。
- `/skill:foo` 给一句 `did you mean /skill foo?`。这是唯一值得为语法加的分支：从 pi 或 Claude Code 迁过来的人最可能打这个，而"unknown command"会让他们以为 atlas 没有这个功能。这句提示排在归档与侧会话两道闸之前，因为"这个命令不存在"不该由会话状态来决定答复。
- 进 `sideCommands`（`cmd/atlas/side.go:37`）与 `archivedCommands`（`cmd/atlas/archive.go:15`）：它不换会话、不换模型，侧会话里只是往当前那个会话问一句。三处帮助文本同步（`sideHelp` / `archivedHelp` / `helpText`），由 `TestSideHelpSaysWhatStillRuns` 与 `TestEveryCommandIsInThePalette` 拉住。归档会话里它只成了一半：列表照跑，加载被拒（第 14 节第二批第三点）。

**不做 `skill` 专用工具。** Claude Code 与本机都有这样一个工具（`Skill` / `skill`）。atlas 不做的理由是名字的成本：`read` 也在模型手上，再加一个工具等于给同一件事两个名字，而模型在"两个都能干"之间的选择不稳定——不稳定在会话里的表现是"有时候读了两遍"，以及提示词里要多写一句"这两个有什么区别"。省下的那一步只是拼绝对路径，而 `location` 已经把路径给全了。

`/skill` 的落法：正文作为一条 user 消息进会话，外面包一层 `<skill name="..." location="...">`。`name` 说明这一份是哪个 skill，`location` 是正文里相对路径的解析基准——没有它，"see references/api.md"要么读错要么读不到。传的是整份文件（frontmatter 也在内），因为那正是模型自己 `read` 会看到的文本，一个文件不该有两种视图。会落盘、可复现，体积被 50KB 上限封顶（第 4 节第 4 条），超限的整份拒绝而不是截断（第 14 节第二批第一点）。v1 不加新的消息类型，用一行 `loaded skill <名字> (<路径>)` 说明加载了哪个 skill，而不是把正文渲染出来——正文是给模型的，不是给读者的。

## 9. 权限与信任

三个问题，一个也不在沙箱范围内，但都得写下来。

**一、skill 不改变权限模型，一点也不。** 这是第 1 节的重复，在这里是结论：装一个 skill 不等于给它什么权限，因为它没有权限可给。它写的每一句"跑这个命令"都要经过和用户手打一样的那道门。所以 README 里不需要 [`mcp.md`](mcp.md) 第 8 节那句"装一个服务器等于给它你给的权限"的对应物；需要说的是反话——skill 是文字，难的是让模型看见它，不是管住它。

**二、skill 是一个能改模型行为的仓库文件。** 项目级 `.atlas/skills` 进 git，意味着 `pull` 一次就能改掉模型被指示做什么。这和 `AGENTS.md` 是同一类风险，而 atlas 对 `AGENTS.md` 的答案是"每轮都读、读不到就静默跳过"——没有签名、没有确认、没有白名单。v1 对 skill 保持同一个答案，但**多一条**：用户级与项目级同名时用户级赢（第 10 节）。理由与 `mcp.json` 项目级只许写 `enabled` 完全相同——仓库不能静默替换一个可信的东西。pi 对同一个风险的解法更进一步：项目里的 `.agents/skills` 要过 project trust 才加载，`~/.agents/skills` 则恒定可信（`trust-manager.ts:181`）。atlas 没有"信任一个项目"这个概念，v1 的替代品就是这一条碰撞规则。

**三、沙箱在这里没有落点，也不需要。** 沙箱约束的是模型写的命令行（`internal/tool/shell` 的 `Wrap`），而 skill 不产生命令行。它产生的是文字，文字经由 `read` 进入上下文，而 `read` 是进程内的工具、不经过沙箱的 `Wrap`——它不执行任何东西，也就没有可约束的东西。这一句写下来是为了挡住一个以后很可能出现的提议："给 skill 一个预批准的工具清单，让它少弹几次审批"。Claude Code 就是这么做的，而且做得很克制——授权只覆盖调用它的那一轮，下一条消息就收回（第 2.2 节）。所以反对的不是这个做法本身，而是它在这里的必要性：atlas 的审批门按"会话 + 工具名"记账，同一个工具问过一次就不再问，本来就没有"每条命令都要点一下"的痛点，多一种作用域只会让"这次为什么问了、下次为什么没问"多一个答案。真正的洞是另一种写法——**把授权写成持久的**，那才是把"skill 是文字"换成"skill 是策略"。

## 10. 每条拒绝与告警

一张表。判据只有一条：**谁能自己修**。

| 现象 | 处理 | 为什么 |
|---|---|---|
| 没有 frontmatter，没有 `---` | 跳过 + 警告带路径 | 根目录下的 `.md` 可能根本不是 skill（第 5 节认散文件，代价就在这里） |
| `name` 缺失 | 跳过 + 警告 | 没名字就没法 `/skill <name>`，也没法进提示词 |
| `name` 不合规（字符/长度/连字符） | 跳过 + 警告带原值 | 名字要能被打出来 |
| `description` 缺失 | 跳过 + 警告 | 没描述模型永远挑不到（第 6 节） |
| `description` >1024 | 截到 1024 + 警告 | 多出来的部分不改变挑选结果，跳过整个 skill 的代价更大 |
| `name` 与目录名不一致 | 接受 + note | 规范要求一致，但拒绝会让一次重命名变成"skill 消失" |
| frontmatter 用了不认的语法 | 跳过 + 警告带行号 | 猜一个子集的语义比报错更糟（第 6 节）；Claude Code 的选择是照样加载、只把字段丢光，而元数据全空的 skill 在提示词里挑不到，等于占着位置又不存在 |
| `allowed-tools` / `disallowed-tools` 声明了 | 接受 + note | 键不生效，但作者以为它生效了；后者更糟——要它拿掉的工具还在 |
| `SKILL.md` 超过 `read` 的上限 | 接受 + 警告带大小 | 被截断的指令比被拒的 skill 更糟（第 4 节） |
| 同名 skill 在两处 | 用户级赢，另一处拒掉 + 警告 | 仓库不能静默替换一个可信的 skill（第 9 节） |
| 目录不存在 | 静默 | 全新安装没有这些目录，警告是噪音 |

一条通则：警告一律带 `skill: <路径>:` 前缀，与 `config: tui: <路径>:` 同型（[`tui.md`](tui.md) 第 7 节的报错对照是同一套做法）。警告不致命——一个坏 skill 不该让会话起不来，正如一台 MCP 服务器连不上只该少掉它自己的工具。

## 11. 不做的事

1. **`context: fork` / 子 agent。** 一个 skill 在独立上下文里跑需要会话树，而 atlas 的 `/side` 是"另一个人跟我说话"，不是"我自己换个上下文"。两者形状不同，混用会让 `/side` 的语义变模糊。
2. **容器与代码执行**（第 2.3 节那半）。atlas 没有容器，也不为 skill 去有一个。
3. **`/reload`。** 这一条原稿的理由是错的：提示词不是每轮重算（第 4 节第 2 条已改），所以它不是"不需要"，而是"缺了"。第一批接受这个缺口，代价写在第 14 节第七点。**0.12.1 起补上了**：提示词每轮重算，`/reload` 是同一套读取的显式入口，报告读到了几个 skill、几个文件（第 14 节第三批）。
4. **`paths` glob 门控**（某些实现里"只在改 `*.py` 时出现"）。它让提示词依赖"这一轮碰过哪些文件"，而提示词本身是每轮重算的——两者乘起来是一个读者猜不出的函数。
5. **MCP prompts 当 skill 的第二个来源。** 形状不同（prompts 是参数化模板，skill 是有正文的文档），而 `/mcp` 已经把 prompts 排除在外了（[`mcp.md`](mcp.md) 第 9 节）。
6. **skill 市场、npm 分发、版本。** `SKILL.md` 进 git 就够了；版本要等"同一个 skill 的两个版本需要同时存在"成为真问题。
7. **递归扫仓库与 `.gitignore`**（第 5 节）。
8. **`license` / `compatibility` / `metadata` 的任何消费者**（第 6 节）。

## 12. 验收

每条都是一条会变红的测试。分三处：`internal/skill` 自己的测试、`cmd/atlas` 的接线与输出、`tests/skills/run.sh`（第 23 个闸门，钉住只有标准库，以及这个包既不能执行文件也不能联网——见第 14 节第二批第六点）。

第一批（已落地），测试名是落地时的实际名字：

1. 发现：含 `SKILL.md` 的目录被认出来，且不再往下探（里面再放一个 `SKILL.md` 不产生第二个）；拼写大小写不同也认，符号链接指过来的也算。→ `TestADirectoryWithASkillFileIsOneSkill` 加 `TestTheSkillFileNameIsFoundWhateverItsCase` 与 `TestALinkedSkillCounts`
2. 散文件：根目录下的 `.md` 也是一个 skill。→ `TestALooseMarkdownFileIsASkill`
3. 四个根与顺序：用户级、共享、项目级、项目共享，名字与 scope 对得上。→ `TestTheFourRootsAreReadInOrder`（`cmd/atlas`）加 `TestTheEarlierRootKeepsTheName`
4. 名字冲突：用户级与项目级同名，用户级赢，诊断里带两个路径。→ `TestTheEarlierRootKeepsTheName`
5. 校验：第 10 节里每一条写着「跳过」的情形各有一个子用例，各自给一条带路径的警告；坏 skill 只警告、会话照常起。→ `TestRejects`（表驱动，十条）加 `TestABadSkillIsReportedAndTheSessionGoesOn`
6. 子集解析：`description: |` 多行拼回、`>` 折行、引号内的 `:` 不切、`#` 注释去掉、未知键（含它下面的缩进块）忽略、锚点与缩进续行报错带行号。→ `TestTheFrontmatterSubset` 加 `TestTheFrontmatterSubsetRejects`
7. 提示词：两个 skill 时 `<available_skills>` 里两条且 `location` 是绝对路径；零个时整段不出现。→ `TestTheSkillDirectoryFollowsTheContext` 加 `TestNoSkillsIsNoBlock`
8. `-no-skills`：提示词与"不带这个开关且没有 skill"时逐字节相同。→ `TestNoSkillsLeavesThePromptAlone`
9. 截断：60KB 的 `SKILL.md` 被接受并警告，行数超限同理。→ `TestASkillLongerThanReadIsAcceptedAndReported` 加 `TestTooManyLinesIsReportedToo`

第二批（已落地）：

10. `/skill` 裸命令：输出含名字、scope、描述、路径；带名字则正文作为 user 消息进会话，带 `<skill name location>` 包装、`args` 接在空行之后；一个 skill 都没有时打出四个根。→ `TestTheSkillCommandListsWhatIsInstalled`、`TestTheSkillCommandLoadsTheBody` 与 `TestNoSkillsSaysWhereToPutThem`
11. `/skill:release` 给 `did you mean`；`/skill deploi` 报错并带上被跳过文件的警告与已装数量；归档会话里列表照跑、加载被拒且不打印 `loaded skill`；超限的 skill 拒绝并说清读在哪停。→ `TestTheColonSpellingIsRedirected`、`TestTheSkillCommandSaysWhatIsMissing`、`TestAnArchivedSessionListsButDoesNotLoad` 与 `TestASkillLongerThanOneReadIsNotLoaded`
12. 侧会话：`/skill release` 的正文进侧会话，主会话一条不多。→ `TestTheSkillCommandLoadsIntoASideConversation`
13. 命令表对齐：`/help`、类型提示与 `commands` 三方一致这条既有断言仍然过，侧会话的调色板数量仍等于 `len(sideCommands)`。→ `TestCommandTableListsTheSessionCommands`、`TestEveryCommandIsInThePalette` 与 `TestThePaletteShrinksInASideConversation`

第三批（0.12.1，提示词每轮重算）：

14. 会话跑着时装的 skill，下一轮就在提示词里；改完的 `AGENTS.md` 同理。→ `TestTheNextTurnListsASkillInstalledMidSession` 与 `TestTheNextTurnReadsTheProjectFileAgain`
15. `/reload` 报告读到了几个上下文文件、几个 skill、多少字节、改没改，并逐个列出读到的文件；带着自己提示词的会话如实说自己那份没法重载。→ `TestReloadSaysWhatItRead` 与 `TestReloadSaysTheSessionCarriesItsOwnInstructions`
16. 两个开关各管一半：`-no-context-files` 不动 skill 名单，`-no-skills` 不动上下文文件。→ `TestTheFlagsLeaveThePromptByteForByte`

## 13. 未解问题

1. **提示词成本的真实数字。** 规范说元数据约 100 token，20 个 skill 就是 2000 token 常驻，而这个客户端的每一次请求都重发全部历史。什么时候需要"只显示前 N 个"或按 scope 过滤，取决于装到多少个之后账单长什么样。规范里没有答案：pi 是到上限就截断，Claude Code 则给列表一个预算（上下文窗口的 1%）并从**最不常用**的 skill 开始丢描述（第 2.2 节）——三种答案对应三种产品阶段的猜测，atlas 还没有资格选。
2. **项目级的上溯。** 第 5 节否掉了。但 mono-repo 里"仓库根一套、某个子包再加一个"是很自然的诉求，而现在只能靠用户级。真正的解法可能是"上溯到最近的一个 `.atlas/skills` 就停"，而不是"每层都读"。等有真实仓库这样用再定。
3. **`description` 的挑选质量。** 模型挑不挑得中取决于描述写得好不好，而描述是用户写的。atlas 能给的最多是一句模板；要不要 lint（比如"描述里必须出现 when / use"）没有证据。
4. **正文超限怎么办。** 第 10 节的处理是"警告但接受"，因为拒绝更糟。但 50KB 读进来仍然是被截断的指令。第二批把它拆成了两半：模型自己 `read` 时仍是截断（那是 `read` 的行为，不属于本功能），`/skill` 时整份拒绝并给出拆法（第 14 节第二批第一点）。剩下的一半个是让 `read` 分页（`offset` / `limit` 已经在参数里，只是 skill 场景没用上），或者让作者把长内容拆进 `references/`。这需要一个真实的坏例子来定。
5. **`/skill` 与自动加载的边界。** 手动 `/skill release` 之后，模型会不会"以为自己是 release 模式"直到会话结束？v1 的答案是没有任何状态（正文就是一条普通 user 消息）。如果实测发现模型会粘住，那需要的是显式作用域（"只对这一个请求"），而那是一个新的消息类型。

## 14. 落地时改了什么

第一批（`internal/skill` 加 `cmd/atlas` 的接线）写完之后，计划里有六处要改，另有四处是计划没提、实现时必须回答的。

### 第一批：发现与提示词

**一、`systemPrompt` 不再是一个函数。** 它本来有 `(path, cwd, noContextFiles)` 三个参数，加一个 `noSkills` 就成了 `systemPrompt("", dir, false, false)`——两个相邻的布尔字面量读不出谁是谁，而错的那一半不会报错，只会让测试断言错的东西。所以把 `systemPrompt` 折进 `promptBuilder.build`：它本来就是"这一轮的提示词由哪些开关决定"那个值，只是过去有两份表达。`build` 现在返回 `(string, []string, error)`，第二个是 skill 的警告——它得能被启动路径打印出来，而重建的那条路（`/init`）没有终端可写，所以不能在里面直接 `Fprintln`。

**二、提示词不是每轮重算的，第 4 节第 2 条写错了。** 那条据此推出"skill 名单不该缓存"，结论碰巧没错（名单确实没缓存），但理由不成立：`build` 只在启动与 `/init` 之后各调一次，得到的字符串交给 `app.Config.SystemPrompt`，之后每轮都用同一份。于是 `/reload` 不是"不需要"而是"缺了"（第 11 节第 3 条已改），代价见第七点。

**三、`read` 的上限是传进来的，不是抄的。** `internal/skill` 不 import `internal/tool`，所以 `Limits{MaxBytes, MaxLines}` 由 `cmd/atlas` 用 `tool.DefaultMaxBytes` 与 `tool.DefaultMaxLines` 填。在 skill 包里写一份 `50 * 1024` 也能过测试，但工具改数字那天这边不会跟着变，而这条警告的全部价值就是它报的数字是真的。

**四、嵌套映射从"报错"改成"跳过"。** 第 6 节原稿把 `metadata:` 下面缩进里的东西列进不认的语法。落地时改主意：`metadata` 是规范定义的扩展位（string→string 映射，写出来必然是一个缩进块），一个合规的 `SKILL.md` 会用它，而"因为有 metadata 所以整个 skill 不加载"是把扩展位变成地雷。规则改成两条：不认识的键连同它下面的缩进块一起跳过；认识的四个键上出现缩进续行才是错误。

**五、第二个 `---` 不是错误。** 第 6 节原稿把"多文档"列进拒绝清单。第一个闭合 `---` 就是 frontmatter 的终点，再出现的 `---` 属于正文——一个用水平线分段的 `SKILL.md` 完全合法，把它当第二个文档会拒掉一份读起来没问题的文件。`TestASecondDocumentIsJustTheBody` 钉住这一点。

**六、名字与目录不一致的 note 只对目录形态生效。** 散 `.md` 文件的"目录名"是那个根目录（`skills`），拿它比对会让每个散文件都报一句"名字与目录不符"，而那正是第 5 节故意允许的形态。

**七、装完 skill 要重开。** 这是第二点的直接后果，也是这一版最实的代价：会话跑着的时候往任何一根目录里加一个 skill，当前会话看不见它。`/init` 会重建提示词，但它重建的理由是 `AGENTS.md` 变了，顺手让新 skill 进来属于巧合。真正的修法是让提示词每轮重算——`app.Config` 收一个函数而不是一个字符串——那是编排层的接口变化，不属于第一批。写在这里是为了让"为什么我刚装的 skill 没出现"有一个能查到的答案。

**八、`SKILL.md` 的查找不分大小写。** 第 5 节写的是"目录含 `SKILL.md`"。实现按 `EqualFold` 比对，因为反过来做的后果是静默的：在 macOS 这种大小写不敏感的文件系统上 `skill.md` 就是那个文件，而 `ReadDir` 报哪个拼写不是作者选的。

**九、`emptyHome` 从此要设 `HOME`。** 共享根是 `~/.agents/skills`，在装了 pi 的机器上那是一个真目录。测试里只设 `ATLAS_HOME` 的话，开发者的提示词与 CI 的不一样，而两边都不算错——这种差异最后会表现成"在我机器上过"。改法是把 `HOME`（以及 Windows 上 `os.UserHomeDir` 读的 `USERPROFILE`）也指到临时目录，顺手让 `emptyHome` 返回它选的 `$ATLAS_HOME`，因为断言 `$ATLAS_HOME/skills` 需要那个路径。

**十、`checkName` 先查字符集再查长度。** 顺序反过来会给一个中文名字报"90 characters"，而真正的问题是它不该有那些字符。

### 第二批：`/skill` 与第 23 个闸门

**一、超限的 skill 在 `/skill` 这里是拒绝，不是截断。** 第 10 节表格写的是"接受 + 警告"，那是启动时扫描的答案：skill 还在表里，用户知道模型会读到半份。`/skill` 不一样，它是我们把正文塞进请求——塞半份等于让模型以为拿到了全部指令，而会话里没有任何一行说明它没有。所以 `Skill.Text` 只检查不截断，拒绝里带上"文件多大、读在哪停、怎么拆"。列表那一半不变，仍然只警告。

**二、注入的正文包一层 `<skill name location>`。** `location` 不是装饰：正文里的 `references/api.md` 需要解析基准，否则模型要么读错要么读不到。`name` 让模型知道这一份是哪个 skill——同一个会话里连开两个是可能的。传整份文件含 frontmatter，理由见第 8 节末。

**三、归档会话里 `/skill` 只成了一半。** 列表是读，照跑；加载是 append 加一轮，被拒。拒绝发生在打印 `loaded skill` 之前——一行 note 报告了一件没发生的事，是这条命令能犯的最糟的错。`archivedCommands` 里仍然要留 `/skill`，否则裸命令会被那句笼统的 `ErrArchived` 一起拒掉，而"在一个收起来的记录里查东西"正是记录存在的意义。

**四、`/skill:foo` 的提示在两道闸之前。** 侧会话与归档会话的闸说的都是"这个状态不许跑这条命令"，而 `/skill:foo` 根本不是命令。先答语法问题，状态不影响答复；这也是唯一能让主会话、侧会话、归档三种情形不必各写一遍判断的地方。

**五、侧会话里加载的是"那边"的正文。** `/skill` 进 `sideCommands`，请求走 `askSide`，主会话一条消息都不多。理由与 `/mcp` 相同：它不换会话、不换模型，只往当前那个会话问一句。`render.setSide(true)` 与 `/side <question>` 走同一处，事件标记才不会落到另一个视图里。

**六、闸门钉的是"skill 是文字"，不是依赖闭包。** 原计划写的是"不可达 `config` / `tool` / `app`"，但 `guard_stdlib_only` 已经覆盖整个闭包，再列一遍是同一条检查的两种说法。更有用的是另一条：`./internal/skill` 不许 import `os/exec`、`net`、`net/http`。"skill 不执行任何东西"是提示词管不住的一句话，而这个包能不能执行由 import 决定。

第二批的验证是 `go test ./...` 与 23 个模块闸门全绿。第一批那次真实二进制的冒烟仍然有效，因为提示词那一半这一批没有动；`/skill` 的端到端行为由上面那些测试覆盖，它们的断言落在 `application.Messages()` 与 `side.Messages()` 上，也就是实际会发给模型的那份文本。

### 第三批：提示词每轮重算（0.12.1）

**一、`app.Config.SystemPrompt` 从字符串变成函数。** `PromptSource` 是 `func() (string, error)`，`cmd/atlas` 那头是 `promptSource`：内核每轮问一次，它每次重读 base、上下文文件与 skill 名单。这条改的是编排层的接口（`app` 与 `agent` 的边界没动，依赖方向没动），换来的是第七点那条代价消失。错误也要有去处：读不出文件不是"跳过"，是这一轮不发出去——模型在没有你确认过的指引下作答，最后表现成一个错的答案而不是一个路径。

**二、会话自带提示词时，源作废。** 从 pi 续来的会话在文件里存了自己的系统消息，那份是它开始时的指令与工具声明，与工具的处理同一条规则：会话保留它开始时的那份。所以 `/reload` 在那种会话里如实说"这份没法重载"，而不是报一次没发生的重载。

**三、`/reload` 是显式的那一半。** 提示词本来就每轮重算，命令的价值在于回答"我刚改的文件到底进没进"：它报读到了几个上下文文件、几个 skill、多少字节、改没改，并逐个列出读到的文件。skill 的警告也在这一行里——启动时打印过一次，之后只有这一条路能再看到。

**四、`/init` 改成当场重算。** 它原来自己重建一次提示词（理由是 `AGENTS.md` 变了），现在调同一条 `ReloadSystemPrompt`：写完的文件当场进提示词，不必等下一轮，而"读不进提示词"这件事在命令自己这一行就报出来。

第三批的验证是 `go test ./...` 与 25 个模块闸门全绿，新增的断言落在实际会发给模型的那份文本上（`prov.Calls()` 里的系统消息）。

## Sources

- Agent Skills 规范：<https://agentskills.io/specification>
- Claude Code skills：<https://code.claude.com/docs/en/skills>（正文读的是 `.md` 变体：<https://code.claude.com/docs/en/skills.md>；HTML 版只有页内示例）
- Building skills 指南：<https://claude.com/resources/guides/the-complete-guide-to-building-skills/fundamentals>
- Agent Skills quickstart：<https://platform.claude.com/docs/zh-CN/agents-and-tools/agent-skills/quickstart>
- Equipping agents for the real world with Agent Skills：<https://www.anthropic.com/research/equipping-agents-for-the-real-world-with-agent-skills>
