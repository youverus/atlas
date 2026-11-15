# Plugins：一个目录，两类贡献

插件在 atlas 里是一件很小的事：**一个目录，一个 `plugin.json`，声明两样东西**——一组 skill，几台 MCP 服务器。它不是程序，没有入口，没有生命周期，没有版本号。这一页说的就是这个格式、它怎么进会话、以及为什么刻意只有这么点。

为什么这么小写在 [`architecture.md`](architecture.md) 第 8 节第 82 条：接口要做，市场不做。理由不是"没时间"，而是 atlas 的权限模型今天只认工具名与命令前缀两级规则，一个能被安装的插件就等于一个能声明任意工具名的第三方——在没有 per-plugin 权限边界之前做市场，等于把"装一个东西 = 交出一份权限"做成一个按钮。

## 1. 目录

```
$ATLAS_HOME/plugins/
  review/
    plugin.json
    skills/
      release/
        SKILL.md
```

- 只有这一个根。**没有项目级插件**：`<cwd>/.atlas/plugins/` 不读。一个 checkout 里能声明插件，等于把任意 argv 交给仓库——与项目级 `mcp.json` 只能开关服务器是同一条理由。
- 目录名就是插件名，字符集是字母、数字、`-` 与 `_`。带点开头的目录跳过，与 skill 的扫描一致。
- 没有 `plugin.json` 的目录不是插件，也不报警告：那个目录允许放别的东西。
- 卸掉一个插件就是删掉它的目录。没有 `enabled` 开关，因为"停用"与"删除"在磁盘上是一件事，两处写法只会有一处忘记。

## 2. `plugin.json`

```json
{
  "description": "Review helpers.",
  "skills": "skills",
  "mcpServers": {
    "lint": {"command": "lint-server", "args": ["--stdio"]}
  }
}
```

三个键，都能省，但**不能全省**：

| 键 | 含义 |
|---|---|
| `description` | 一行话，`/plugin` 的表格里那一列。只给人看。 |
| `skills` | 插件目录内的相对路径，指向一组 skill（一个目录里若干 `<name>/SKILL.md`）。复用 [`skills.md`](skills.md) 的目录约定。 |
| `mcpServers` | 与 `mcp.json` 里那个对象**同一个形状**：键是服务器名，值是一个 `mcpEntry`。 |

约束：

- **严格解码。** 未知键报错——`"version": "1.0.0"` 会被拒，不是忽略。原因与 `config.json` / `tui.json` / `mcp.json` 一样：一个不生效的键比没有这个键更糟，作者以为它起作用。
- **只允许一个 JSON 值**，且受 `maxSize`（1 MiB）限制。手写文件的三种老毛病（粘贴两遍、留个多余的花括号、把整份 models.dev 抄进来）各有各的错法，都不该静默。
- **两样都不声明就是拒绝。** `{"description": "..."}` 报"contributes nothing"：一个什么都不加的插件更可能是写了一半，而不是值得静默加载。
- **`mcpServers` 的每一项走 `mcp.json` 同一套校验**（`mcpEntry.validate`）：要么 `command`（stdio）要么 `url`（streamable HTTP），不能都是；`auth: "oauth"` 必须有 `url`；``${NAME}`` 展开与 `Missing` 记录也是同一份代码。所以插件里的服务器写法与 `mcp.json` 里的**一字不差**，`docs/mcp.md` 第 7 节的配置形状整份适用。

## 3. 路径规则

`skills` 是相对路径，解析发生在读出 `plugin.json` 之后、扫目录之前，且是**词法检查**而非 `realpath`：

- 绝对路径拒绝（`"/etc"`）。
- 规范化之后以 `..` 开头的拒绝（`"../review/skills"`）。
- `.` 拒绝：插件目录本身当 skill 根没有意义（`skill` 的扫描不下探到根上的 `SKILL.md`）。

符号链接不解析、也不拒绝，这与 `internal/skill` 的取舍一致：把一个目录指到别处是用户自己做的安排，做在他看得见的地方。

## 4. 两类贡献怎么进会话

### skill

插件里的 skill 目录按插件名排序，作为**新的一档根**插进既有的五个根之间：

```
$ATLAS_HOME/skills          user
~/.agents/skills            shared
$ATLAS_HOME/plugins/*/skills  plugin      ← 这一档是本轮新增的
<cwd>/.atlas/skills         project
<cwd>/.agents/skills        project-shared
```

撞名规则与其他根没有区别：**先到者赢，后来者出一条 note**。所以用户自己写的 skill 压过插件带来的同名 skill，插件压过 checkout 带来的。`/skill` 的 SCOPE 列写 `plugin`，LOCATION 列是完整路径——"这个是哪个插件带来的"由路径回答，不额外加一列插件名。

插件可以只声明 `skills` 而没有任何服务器，反之亦然。

### MCP 服务器

折叠顺序是 `mcp.json` → 插件（按插件名、插件内按服务器名）→ 项目文件：

- 项目文件仍然**只能开关**，但可以开关一个插件声明的服务器（`{"mcpServers": {"lint": {"enabled": false}}}`）。这是新出现的一种组合，也是最有用的一种：仓库可以说"这一台先别连"。
- 撞名的处理与 skill 相同：**先到者赢并出一条 note**，不是致命错误。这是与 `mcp.json` 内部规则的有意区别——那个文件是用户正在编辑的那一份，一个坏条目该拒绝整份；而插件是扫描发现的若干目录之一，一个撞名不该让整台机器的服务器都起不来。
- 名字只差 `-` 与 `_` 也算撞名（两者会产出同一个 `mcp__` 前缀），note 里把两个名字都写出来。**同一个插件文件内部**的 `-`/`_` 撞名仍是致命的，与 `mcp.json` 一致：那时前缀冲突就在一个作者手里。
- 插件声明工具的路径与别的服务器没有区别：`mcp.Open` 拿到的 `Spec` 一模一样，所以工具进同一张表、同一套审批门、同一套截断。

合并后的服务器列表由 `config.MCPConfig` 表示，`Notes` 字段带着"哪台被跳过、为什么"。`cmd/atlas` 的 `mcpFor` 把它当成启动时的 warning 打印——包括插件本身的坏文件，所以一个写坏了的插件在启动时就有一行，不必等到 `/plugin`。

## 5. 一条坏插件的代价

**只损失它自己的贡献。** 一个 `plugin.json` 解不开、声明的 `skills` 目录不在、服务器条目写错、目录名不合规——每种都只产生一行 `plugin: <路径>: ...`，其余插件照常加载，会话照常启动。这与 `mcp.json` 的"一份文件里一个坏条目就拒绝整份"是一条刻意的分歧，理由在上一节。

两种问题的粒度也不同：

- **声明本身坏了**（路径跑出目录、服务器两样传输都有）→ 整个插件不加载，因为它声明的东西无法解释。
- **声明指向的东西不在**（`skills` 目录被删了）→ 插件仍然加载，只是这一类贡献为空；服务器那一半可能还有用。

## 6. `/plugin`

无参数，只读：

```
NAME    SKILLS  SERVERS    DESCRIPTION       LOCATION
review  1       docs, lint Review helpers.   /Users/me/.atlas/plugins/review
```

- **SKILLS 是这次会话实际拿到的数量**，不是目录里有多少：被用户自己的同名 skill 压掉的插件 skill 不计入。哪一个被压掉了写在 `/skill` 的 note 里。
- **SERVERS 列名字而不是数量**："我调不通的那台是不是这个插件带来的"是这一列被问的问题。
- 一个插件都没有时打印 `$ATLAS_HOME/plugins` 这个路径，"该放哪儿"是那一刻真正的问题。
- 插件的服务器**在启动时连接**，技能在每次 `/skill` 与每轮提示词里重读，所以 `/reload` 能捡到新的 skill，服务器要重开。
- 命令不写会话，所以它在归档会话与侧会话里都照跑（`archivedHelp` / `sideHelp` 两行同步说明了这一点）。

## 7. 不做的事

四件是第 82 条点名的，本轮全部保持不做：

- **不做市场。** 上架、评分、搜索、安装按钮。重新评估的条件是 per-plugin 权限边界先落地（插件的工具与 skill 能被规则单独放行或拒绝），那时市场才是"分发"而不是"授予"。
- **不做远程安装。** 装一个插件的动作是 `git clone` 到那个目录，或复制一个目录进去。atlas 没有下载器，`internal/config` 也不许 import `net`——`tests/config/run.sh` 有一条闸门钉住这件事。
- **不做版本与依赖解析。** 一个插件不声明它需要什么，`plugin.json` 里写 `version` 会被判成未知键。
- **不做签名与信任链。** 谁发布的、改过没有，atlas 都不回答；它与 `~/.agents/skills` 里的文件、`AGENTS.md` 是同一类东西：用户放进来的文字与配置。

还有四件本轮同样不做，写下来免得被当成遗漏：

- **插件不能加命令、钩子或工具。** 边界就是 skill 与 MCP 服务器两样。一个能注册 `/something` 的插件需要一套命名空间与冲突规则，那是另一个功能。
- **没有项目级插件**（第 1 节）。
- **没有 enable / disable**（第 1 节：删目录就是停用）。
- **没有 per-plugin 的权限边界。** 插件声明的服务器进的是同一张工具表，审批规则看得见工具名，看不见"它来自哪个插件"。

## 8. 验收

每条断言都在测试里，位置在括号内。

1. 一个插件被发现，两类贡献都读得到（`internal/config` 的 `TestLoadPluginsReadsTheTwoContributions`）。
2. 没有插件目录时不报错、不报警告，但路径仍然给出（`TestLoadPluginsIsQuietWithoutADirectory`）。
3. 没有 `plugin.json` 的目录不算插件，且不出声（`TestLoadPluginsIgnoresADirectoryThatIsNotAPlugin`）。
4. 十种写坏的 `plugin.json` 各拒绝一次并指名文件（`TestLoadPluginsRefuses`）。
5. `skills` 目录不在时保留服务器那一半（`TestLoadPluginsKeepsTheServersOfAMissingSkillsDirectory`）。
6. 一个坏插件不影响别的插件（`TestLoadPluginsKeepsTheGoodOnes`）。
7. 插件声明的服务器进 `MCPConfig.Servers`，``${NAME}`` 照常展开（`TestLoadMCPFoldsPluginServers`）。
8. 插件服务器与 `mcp.json` 的条目走同一套校验（`TestAPluginServerIsValidated`）。
9. 项目文件能关掉一台插件服务器（`TestAProjectFileCanSwitchAPluginServerOff`）。
10. `mcp.json` 赢同名，含只差 `-`/`_` 的情形，两种都出 note（`TestTheHomeFileWinsAServerName`、`TestANearMissOnAServerNameIsNoted`）。
11. 没有 `mcp.json` 时插件是唯一来源（`TestPluginsAreTheOnlySource`）。
12. 插件里的服务器可以声明为关（`TestAPluginServerCanBeDeclaredOff`）。
13. 坏插件在 MCP 这条路上是一行 note 而不是错误（`TestABrokenPluginIsANoteOnTheMCPPath`）。
14. 插件 skill 根排在 shared 之后、project 之前，且撞名时插件赢 checkout（`cmd/atlas` 的 `TestAPluginSkillRootSitsBeforeTheProject`）。
15. 只声明服务器的插件不产生 skill 根（`TestAPluginAddsNothingUnlessItHasSkills`）。
16. `/plugin` 的三张脸：有插件、无插件、有坏插件（`TestPluginListsWhatIsInstalled`、`TestPluginSaysWhereToPutOne`、`TestPluginReportsABrokenOne`）。
17. `/plugin` 不收参数（`TestPluginTakesNoArgument`），且在两个只读状态里仍在白名单上、两行帮助文本同步（`TestPluginRunsInEveryState`）。
18. SKILLS 列按插件目录计数，不把兄弟目录算进去（`TestTheSkillCountIsPerPluginDirectory`）。

`tests/config/run.sh` 多一条闸门：`internal/config` 不许 import `os/exec` / `net` / `net/http`——插件声明一条命令行，而它永远不执行它。

## 9. 落地时改了什么

写这页之前的设计与最终代码有两处不同：

1. **`plugin.json` 从"指向一个 `mcp.json`"改成内联 `mcpServers`。** 原计划多一个文件、多一条路径要校验。内联之后 `mcpEntry` 直接复用，插件作者写的形状与 `mcp.json` 一模一样，也没有第二个路径逃逸面。代价是 `plugin.json` 同时承担"标记文件"与"配置"两个角色，可接受。
2. **`LoadMCP` 的旧早退被删掉了。** 原来 `mcp.json` 不存在就直接返回，于是项目文件也不会被读。今天插件可能就是唯一的来源，所以"全局文件缺失"只意味着这一来源为空，后面的两个来源照读。副作用是：一个只有项目级 `mcp.json`、没有全局文件的仓库，现在会在启动时报出项目文件里那个不存在的名字——过去它被静默忽略。这是更正确的一边：那本来就是一个拼写错误。
