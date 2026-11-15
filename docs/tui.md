# tui.json 配置参考

这份文件管**界面临什么样**：配色、间距、几个框的宽度、要不要首页。它不管"请求发给谁"——模型、端点、凭据都在 `config.json` 里。两者的连接只有一行：

```json
{"tui": "tui.json"}
```

想要"改什么都别改代码"的话，需要查的就是这一页。为什么分成两个文件、为什么某些决定是这个样子，见 [architecture.md](architecture.md) 第 3 节与第 8 节第 61 条。

## 1. 文件在哪、怎么被引用

| 情形 | 行为 |
|---|---|
| `config.json` 里写 `"tui": "tui.json"` | 按**相对 `config.json` 所在目录**解析，与在哪个目录启动 `atlas` 无关 |
| `"tui": "~/dotfiles/atlas-tui.json"` | `~` 展开成家目录，和其他路径一样 |
| `"tui": "/etc/atlas/tui.json"` | 绝对路径，原样使用 |
| 不写 `tui` 这个键 | 不读任何外观文件，全部用内置缺省（也就是 0.4.10 之前的样子） |
| `"tui": ""` 或全是空格 | **报错**。空串和"不写"是两件事，静默当成一回事会让"为什么我的配置没生效"变成一个谜 |
| 写了但文件不存在 | **报错**并给出解析后的完整路径，不静默退回缺省 |

首次运行 `atlas` 时，它会在 `~/.atlas/` 写下 `config.json`、这份 `config.json` 引用的 `tui.json`（两者都是 `0600`）以及一份 `config.example.json`。先写 `tui.json` 再写 `config.json`，所以不会出现"配置指向一个还不存在的文件"的瞬间。已经存在的文件一律不覆盖。

`tui.json` 与 `config.json` 用同一套解码规矩：未知字段直接报错、只允许一个 JSON 值（尾随数据报错）、上限 1 MiB。拼错 `colour` 不会安静地什么都不改，而是报 `json: unknown field "colour"`。

## 2. 文件形状

顶层四个键，都可以省略：

```json
{
  "theme": "auto",
  "colors": {"accent": "#ff8800"},
  "blocks": {"transcript": {"gap": 1, "frames": true}, "composer": {"gapAbove": 2, "frame": false}, "status": {"gapAbove": 1}},
  "roles": {"thinking": {"rows": 8}}
}
```

```text
~/.atlas/
├── config.json     ← 模型、端点、凭据；只写 {"tui": "tui.json"}
├── tui.json        ← 本页说的这个：theme / colors / blocks / roles
├── config.example.json
├── themes/         ← 想自己写调色板时放这里（第 3 节）
└── sessions/
```

## 3. theme：整体配色

`theme` 一个字符串，三种写法：

| 取值 | 结果 |
|---|---|
| 不写，或 `""`，或 `"auto"` | 按终端背景在 `atlas-dark` / `atlas-light` 之间挑 |
| `"atlas-dark"` / `"atlas-light"` | 直接用内置的那套 |
| `"<名字>"` | 找名为 `<名字>.json` 的主题文件 |

`auto` 目前**一律按深色终端处理**（还没去问终端自己的背景色），所以今天它等价于 `atlas-dark`。浅色终端请直接写 `"atlas-light"`。

两套内置配色自带真底色（`atlas-dark` 是 `#1a1b26`，`atlas-light` 是 `#e1e2e7`），所以 0.4.15 起打开 `atlas` 会看到整块屏幕被刷成主题的底色，而不是终端自己的背景；输入区那一圈框里再亮一档（第 4 节）。

`theme` 只接受名字，不能是路径：`"../atlas-dark"`、`"."`、带 `/` 或 `\` 的值在读配置时就报错（`is a name, not a path`）。主题文件的查找由主题层负责，它还会在报错里列出它找到的名字。

### 3.1 自己写一套主题

主题文件放在这两处，同名的由近的覆盖远的：

```text
<ATLAS_HOME>/themes/<名字>.json          全局，默认就是 ~/.atlas/themes/
<项目目录>/.atlas/themes/<名字>.json      项目内（覆盖全局同名）
```

"项目目录"是**启动 `atlas` 时所在的那一层**，所以同一个主题名在不同项目里可以是不同的文件。第一处的父目录是 `ATLAS_HOME`，不是 `-config` 指的那份文件所在目录：`-config` 只搬 `config.json` 与它引用的 `tui.json`，主题目录仍由 `ATLAS_HOME`（缺省 `~/.atlas`）决定——想把主题一起搬走，连 `ATLAS_HOME` 一起改。

文件名（去掉 `.json`）就是 `theme` 里要写的名字。格式：

```json
{
  "version": 1,
  "mode": "dark",
  "extends": "atlas-dark",
  "colors": {
    "accent": "#ff8800",
    "user": "#ffd479"
  }
}
```

- `version` 必填，当前只认 `1`。
- `mode` 必填，`"dark"` 或 `"light"`。它决定这套配色在 `auto` 的深浅判断里算哪一边。
- `extends` 可选。写了就继承另一个主题，`colors` 只需要写想改的那几个 token。
- 不写 `extends` 时 `colors` 必须**写全十二个 token**，少一个都报错并列出缺的是哪几个。
- 任何一个文件写坏（未知字段、拼错 token、颜色格式不对、继承成环或超过八层）都不会拦住启动：`atlas` 在启动时把它打到 stderr（`theme: <名字>: <原因>`），然后让这个名字不可用。但如果 `theme` 里**选中的**正是这个名字，启动就会失败并指名——只有这一种"用不了"值得拦。

十二个 token 的名单与各自作用见下一节，主题文件与 `colors` 用的是同一份词汇、同一张校验表。

## 4. colors：只改当前主题里的某几个 token

`colors` 是"叠在主题上"的一层，不是第二套主题：只换你点名的 token，其他照旧来自主题，`auto` 事后挑中另一套配色也照样只换这几个。图为 `"colors": {"accent": "#ff8800"}` 只动强调色。

| token | 画在哪 | 今天有绘制点 |
|---|---|---|
| `text` | 回答正文（含表格、列表、加粗与斜体）、输入行里真正打的字、审批框正文、菜单与命令面板里未选中的标签 | 有 |
| `dim` | 欢迎页的说明行、思考块的标题与正文、菜单/面板里的次要说明（`note`）、审批框的按键提示、回答里的引用块与分隔线 | 有 |
| `accent` | 回答左侧那条竖线 `│`、回答里 Markdown 的标题与链接、输入行的 `>` 与占位提示、欢迎页的字标、菜单/面板的选中标记与标题、审批框标题 | 有 |
| `border` | 输入区那一圈圆角框、一段思考/调用的盒子框线、审批框与菜单框的边框 | 有 |
| `user` | 自己打的那一行（`> ` 与正文） | 有 |
| `tool` | 工具调用的那一枝（名称、参数、结果树）；框起来之后是盒子标题与每一行调用 | 有 |
| `error` | 错误块、失败的工具结果 | 有 |
| `ok` | 成功状态 | 暂无 |
| `warn` | 警告状态 | 暂无 |
| `code` | 回答里 Markdown 的代码块与行内代码 | 有 |
| `selection` | 选中态的背景 | 暂无 |
| `background` | 整屏背景；输入区框内的底色由它按 8% 推一档。段落盒子没有自己的一层，框内就是整屏背景 | 有 |

"暂无"的三项属于调色板的词汇：写在主题文件或 `colors` 里都能通过校验，也不会报错，只是当前界面还没有用到它们的元素，所以屏幕上不会变。等有了对应元素，它们才会开始起作用。`code` 曾经也在这张名单里，0.12.4 起回答按 Markdown 绘制，它落到了代码块与行内代码上。0.12.15 的拖选复制是 `selection` 的一个反例，不是它的第一个用户：高亮按字形插反显 `\x1b[7m` / `\x1b[27m`，因为反显在只认十六色的终端上也成立，而这个槽位的颜色只在真彩终端上才看得出差别。

`background` 是唯一一个"画两处"的 token：整屏用它，输入区框里的那一层用它朝终端另一头（深色主题朝白、浅色主题朝黑）混合 8% 得到（`Theme.Surface()`，系数在 `internal/theme` 的 `surfaceLift`）。混合的方向取自主题的 `mode` 而不是背景自身的亮度——一套 `mode: "dark"` 的配色即使把背景写成浅灰，凸起仍然该朝亮处走，"这个界面被当成深色画"是主题声明的事，不该由某一个色值反推。`"default"` 时原样返回，两层都不涂，等于关掉整块底色。回答里的 Markdown 片段也各自带着它：glamour 写出的每一段样式都以 reset 结尾，而 reset 会把格子还回终端自己的底色——不写进去，一段带颜色的字就会在整屏背景上打出一个洞。**没有第十三个 token**：允许单独写"凸起色"就等于允许写出"凸起比屏幕更暗"这种一眼能看出来的错，而那个错没有任何一句话能解释它为什么该被允许。

值只有两种形状：

| 写法 | 含义 |
|---|---|
| `"default"` | 不涂，让终端自己的前景/背景透出来 |
| `"#rgb"` / `"#rrggbb"` | 十六进制，三位的会展开成六位（`#ABC` → `#aabbcc`） |

`"blue"` 这种颜色名不被接受：同一台机器上两个终端对 `blue` 的理解可以不一样，而十六进制不会。

颜色**不在读 `config.json` 时报错，而在启动那一刻**报——它要和主题文件比同一张表，而读配置的那一层（`internal/config`）刻意不依赖主题层。所以两条错出现的时间不同：布局的错在读文件时当场报，颜色的错在解析主题时报，两条都指名。

## 5. blocks 与 roles：按屏幕块命名的数字

`blocks` 是屏幕上看得见的一块地方，`roles` 是 transcript 内部的正文块。分开是因为它们回答的问题不同：一个块**从屏幕拿走**行数（`rows`、`width`、四周的 `gap`），一个 role 只决定**已经在那儿的那几行**里画什么，不参与屏幕的算术。transcript 是唯一没有自己数字的块——别人剩下的都归它，所以它的两个键都关于它**内部**：两块之间留几行，以及一段要不要框起来。

全部可选。只写想改的键，没写的保持内置缺省；**写 `0` 和不写是两件事**（`"gapAbove": 0` 是"贴着"，不写是"用缺省"）。

`blocks.*`：

| 键 | 缺省 | 范围 | 作用 |
|---|---|---|---|
| `blocks.transcript.gap` | 1 | 0–4 | 两块对话之间几行空白。**要不分块不是设置**：那是 prose 与工具树的分界 |
| `blocks.transcript.frames` | `true` | 布尔 | 把一段思考、一段连续的工具调用各自框进一个盒子，标题写这段是什么（`Thought for 3.1s` / `2 calls · read, grep`）。每个盒子花 2 行框线；`false` 就是 0.4.17 的样子，一块一行 |
| `blocks.composer.gapAbove` | 1 | 0–8 | transcript 与输入区之间几行空白。命令面板打开时，这几行在面板**之上**（面板属于正在打的那一行）；`0` 就是贴着 |
| `blocks.composer.rows` | 8 | 1–24 | 输入行最多长到几行，超过就从 transcript 借空间 |
| `blocks.composer.frame` | `true` | 布尔 | 输入区那一圈圆角框画不画。它占 2 行 4 列；`false` 就是 0.4.14 的样子 |
| `blocks.palette.rows` | 5 | 0–20 | 命令面板最多几行，超出滚动；`0` 关掉面板 |
| `blocks.status.gapAbove` | 0 | 0–4 | 输入区与状态栏之间几行空白。`0` 是状态栏紧贴提示语 |
| `blocks.menu.rows` | 10 | 1–40 | `/model` `/resume` 那张菜单最多几行，超出滚动并在标题写 `3–12 of 20` |
| `blocks.menu.width` | 72 | 20–200 | 菜单框宽度（含边框），屏幕放不下时照旧让位 |
| `blocks.approval.width` | 64 | 20–200 | 审批框宽度（含边框），缺省比菜单窄：问题是一句话，列表有三列 |
| `blocks.welcome.on` | `true` | 布尔 | 空会话画不画欢迎页；`false` 就是一块空屏加一个提示符 |

`roles.*`：

| 键 | 缺省 | 范围 | 作用 |
|---|---|---|---|
| `roles.thinking.rows` | 8 | 0–40 | 思考正文留几行尾巴（永远留尾巴，不留开头）；`0` 只留标题与时长 |

屏幕高度的账是这样算的（`bodyHeight()`）：

```text
transcript 行数 = 窗口高 − palette.rows − composer.gapAbove − 输入行 − status.gapAbove − 1（状态栏）
输入行 = composer.rows（按内容伸缩后的实际行数）+ frame ? 2 : 0
```

`transcript.frames` 同理：它不是"好不好看"而是"占几行"。一段一个盒子，每段两条框线，所以一轮里有五段交替就等于额外花掉 10 行——24 行的窗口里那几乎是半屏。关掉它就退回 0.4.17 的一行一块（这一路的帧是逐字节对拍过的）。

`frame` 之所以在 `blocks` 而不是在主题里：它是一个几何开关，不是颜色。它让输入行的换行宽度少 4 列（`composerWidth`）、让 caret 向右下各挪一格、让 transcript 少 2 行——这些都得从同一账号本里扣，和 `rows` 是一类东西。框的**颜色**仍归主题：边框用 `border`，框内底色用 `background` 推出的一档，正文用 `text`，`>` 与占位提示用 `accent`。宽度低于 8 列时整块框不画（`2×2` 边框 + 4 列文字才是能打字的最小值）：两格的框不是更小的框，是坏的框。

所以 gap 与输入行都是**从 transcript 借**的，不会把帧画到窗口外面去。越界的数在读 `tui.json` 时报错并写全路径与区间（`blocks.status.gapAbove: 9 is outside 0–4`）；界面那一层只做钳制，因为一个在代码里拼出来的 `Layout` 该得到一帧画得出来的屏幕，而不是一条关于行数的错误。

### 5.1 三个现成的配方

不要一样一样试，抄一份再微调：

```json
// 紧凑：24 行的窗口，尽量多的对话
{"blocks": {"transcript": {"gap": 1, "frames": false}, "composer": {"gapAbove": 0}, "palette": {"rows": 3}, "status": {"gapAbove": 0}}, "roles": {"thinking": {"rows": 3}}}
```

```json
// 宽松：60 行以上的窗口，回答之间留气口
{"blocks": {"transcript": {"gap": 2}, "composer": {"gapAbove": 2, "rows": 12}, "menu": {"width": 90}, "status": {"gapAbove": 1}}, "roles": {"thinking": {"rows": 12}}}
```

```json
// 极简：只有对话、输入行与状态栏
{"blocks": {"composer": {"gapAbove": 0, "frame": false}, "palette": {"rows": 0}, "status": {"gapAbove": 0}, "welcome": {"on": false}}, "roles": {"thinking": {"rows": 0}}}
```

## 6. 什么时候生效

启动时读一次，改完要重开 `atlas`（`/model`、`/resume` 之类都只在进程内切，不会重读文件）。所有检查都在第一帧画出来之前完成——配色或布局写坏时你看到的是终端里的一行报错，而不是一个用缺省值画出来的界面。

## 7. 报错对照表

| 你看到的（节选） | 意思 | 改哪里 |
|---|---|---|
| `config: tui: /path/tui.json: json: unknown field "colour"` | 键名拼错 | `tui.json` 的那个键 |
| `config: tui: /path/tui.json: blocks.status.gapAbove: 9 is outside 0–4` | 数字越界 | 同一行上的数值 |
| `config: tui: /path/tui.json: blocks.composer: json: unknown field "gap"` | 块里的键名拼错，错误写全块路径 | 那个块里的键 |
| `config: tui: /path/tui.json: json: unknown field "layout" (the geometry is grouped under "blocks", …)` | 0.4.12 及更早的写法：几何是一整个 `layout` 对象 | 按第 9 节拆成 `blocks` / `roles` |
| `config: tui: /path/tui.json: json: unknown field "composerGap" (that key is now blocks.composer.gapAbove)` | 只搬了一半：外壳删了，扁平键还留着 | 照提示里的路径塞进对应块 |
| `config: tui: /path/tui.json: theme "../x" is a name, not a path; …` | 把路径当成了主题名 | `theme` 只写名字，文件放进 `themes/` |
| `config: tui: colors: accent: "blue" is not "default" or a hex color like "#7aa2f7"` | 用了颜色名 | 换成 `#rrggbb` 或 `default` |
| `config: tui: colors: acccent is not a token; this build knows: …` | token 拼错（并且会列出全部十二个） | 对照第 4 节的表 |
| `config: tui: open /path/tui.json: no such file or directory` | `tui` 指的文件不存在（路径按 `config.json` 所在目录解析） | `tui` 那一行，或补上文件 |
| `config: tui: "tui" is empty: name a file, or leave the key out …` | 写了键却没给值 | 删掉这个键，或写一个文件名 |
| `config: /path/config.json: json: unknown field "theme" (theme, colors, blocks and roles live in the file that "tui" names)` | 0.4.10 的旧写法：`theme` / `ui` 已经搬走了 | 把这两块搬到 `tui.json`，`config.json` 只留 `"tui"` |
| `theme "nope" is not defined; this build has: atlas-dark, atlas-light` | 选了一个不存在的主题名 | `theme` 的值，或把主题文件放回 `themes/` |

`theme: <名字>: <原因>` 这一行不是错误，是提醒：某个主题文件坏了、这个名字已被停用，但界面照常用别的配色起来了。

## 8. 这里不该放什么

| 你想改的 | 该去哪儿 |
|---|---|
| 默认模型、端点、凭据、思考级别、会话目录 | `config.json`（见 [README](../README.md#配置)） |
| 调色板的具体十二个色 | 主题文件（第 3.1 节）。`colors` 只适合改两三个 token |
| 键位 | 代码。目前没有可配置的键位 |
| 状态栏显示哪几段 | 代码。状态栏按宽度自动降级（`cmd/atlas/status.go`） |
| 欢迎页显示什么 | `blocks.welcome.on` 只能开关；那几行事实来自宿主（版本、目录、模型、权限） |

## 9. 从旧配置搬过来

0.4.10 及更早，外观写在 `config.json` 里：

```json
{
  "theme": "auto",
  "ui": {"colors": {"accent": "#ff8800"}, "layout": {"composerGap": 2}}
}
```

搬法是照抄到 `tui.json` 并去掉 `ui` 这一层：

```json
{
  "theme": "auto",
  "colors": {"accent": "#ff8800"},
  "layout": {"composerGap": 2}
}
```

`config.json` 里那两处删掉，换成 `"tui": "tui.json"`。旧键现在会被当成未知字段拒绝并给出提示。

0.4.12 及更早的 `tui.json` 把几何写成**一个扁平的 `layout` 对象**，0.4.13 起按屏幕块命名：

```json
{"layout": {"composerGap": 2, "statusGap": 1}}
```

改成：

```json
{"blocks": {"composer": {"gapAbove": 2}, "status": {"gapAbove": 1}}}
```

对照关系（键值不变，只是换了位置与名字）：

| 0.4.12 | 0.4.13 |
|---|---|
| `blockGap` | `blocks.transcript.gap` |
| `composerGap` | `blocks.composer.gapAbove` |
| `composerRows` | `blocks.composer.rows` |
| `paletteRows` | `blocks.palette.rows` |
| `statusGap` | `blocks.status.gapAbove` |
| `menuRows` | `blocks.menu.rows` |
| `menuWidth` | `blocks.menu.width` |
| `boxWidth` | `blocks.approval.width` |
| `thinkingRows` | `roles.thinking.rows` |
| `homePage` | `blocks.welcome.on` |

`theme` 与 `colors` 不动，仍在顶层。旧键会被拒绝，并且报错会写出新路径；只搬一半（删了 `layout` 外壳、留着扁平键）同样会被拒绝并逐键指路。

`blocks.transcript.frames` 是 0.4.18 新加的键，没有旧名字可对照：不写就是 `true`（一段一框）。所以升级后**已有的 `tui.json` 会自动开始画盒子**，原本一块一行的对话会变高——24 行的窗口、一轮五段，等于少了 10 行对话。不想失去那几行就写 `"frames": false`，帧与 0.4.17 逐字节相同。

`blocks.composer.frame` 是 0.4.15 新加的键，没有旧名字可对照：不写就是 `true`（画框）。同一版里 `background` 从"暂无绘制点"变成了"画整屏"，所以**自己写的主题会突然开始有底色**——它们如果写的是 `"background": "default"` 就什么都不变，写的是十六进制则从此会真的刷满全屏，输入区框内更亮一档。
