# 模型目录：数据、优先级与 clamp

这份文档有两半。前六节是依据与规则：表里放什么、为什么是嵌入的 JSON、声明与表谁说话、档位怎么夹。第七节是验收，第八节是不做的事，第九节留给落地之后。

标注约定：**【spec】** 厂商文档或 models.dev 的数据；**【pi】** TypeScript 参考实现的做法；**【atlas】** 我们自己的决定。

## 1. 表里放什么

九条种子，三个 provider（`anthropic` / `deepseek` / `openai`），每条八个事实：provider、id、显示名、上下文窗口、输出上限、能否思考、输入模态、收窄后的思考档位。

**【spec】** 数字取自 `https://models.dev/api.json` 的 2026-11-14 快照，逐条用 `jq` 核对过。它是社区维护的聚合表，不是厂商文档；一个窗口数字错了的表现是"压缩阈值不对"，不是"请求失败"，所以它属于可以修的数据，不属于会让程序崩的契约。

**【atlas】** 九条是种子，不是上游镜像。整份 models.dev 有几百个模型、六兆字节，全抄进来的代价是每次刷新一大片 diff、其中绝大多数没人会用；而 `//go:embed` 一个手写文件让"窗口写错了"变成一份可 review 的数据 diff。整份刷新留给将来的 `cmd/atlas-catalog` 生成器，它要做的事是拉一次 API、映射成同一种形状、丢掉 `pdf`/`audio`（`wire.Content` 没有那些类型，声明了只会让适配器静默丢数据）、写回同一个文件。

**【atlas】** 数据是 JSON 而不是生成的 Go：生成的 Go 会把数据和逻辑焊在一个文件里，而这两样东西的审核者不是同一个人。

## 2. 为什么目录是叶子

`internal/catalog` 只依赖标准库，不 import `internal/wire`，也不 import `internal/config`。

- **不 import `wire`**：把一条 `Entry` 变成 `wire.Model` 是一个结构体字面量，属于知道"这个进程要发什么请求"的组合根（`cmd/atlas`）。目录知道 gpt-5 的窗口是 40 万，不需要知道我们的消息长什么样。
- **不 import `config`**：七级词汇表在两边各写一份。重复是刻意的：目录要被"没有配置文件"的测试驱动，而 `config` 要被"没有目录"的测试驱动。两处同名字符串的漂移会被 `tests/cli` 的接线用例当场抓到。

`tests/catalog/run.sh` 用 `guard_stdlib_only` 守住这条线，`tests/deps/run.sh` 从另一头确认它的闭包里没有第三方模块。

## 3. 优先级：声明 > 表 > 零值

一个字段只有一个说话的地方，顺序固定：

| 字段 | 谁说话 | 都不说话时 |
|---|---|---|
| `name` | 配置的 `name` | 表里的显示名，再退 id |
| `contextWindow` | 配置的值（含 `0`：明确不自动压缩） | 表里的窗口 |
| `maxTokens` | 配置的值 | 表里的输出上限 |
| `input` | 配置的列表 | 表里的模态 |
| 思考档位轴 | 配置的 `thinkingLevels` | 表的 `thinking`，再退七级全档 |
| `reasoning` | 只有配置 | **表不补** |

**【atlas】** 前五行是同一个理由：声明是"这个端点"的陈述，零值是"没说"。一个挂在代理后面的 `gpt-5` 可能确实只有十万窗口，用户写了就以用户为准；没写时用厂商文档的数字，比用一个 0（关掉自动压缩）更接近用户的意思。

**【atlas】** `reasoning` 反过来，因为 `false` 不是空缺而是陈述。它在 JSON 里区分不出"我没写"与"我写了 false"，而它的语义是"这个端点会不会接受思考字段"——目录替它打开，会让一个只是借用了 vendor id 的网关开始收到它不认识的参数并拒绝请求。所以目录永远不打开它，但会在用户已经声明 `reasoning: true` 之后收窄档位轴。**代价**是一个只写 id、不写 `reasoning` 的配置拿不到表的档位轴；现在的 CLI 本来也要求显式声明，所以不是新缺口。

**【atlas】** 查找只按裸 id，不按 provider。配置里的 provider 名是用户给端点起的标签（`openai` 指向一个代理仍然在服务 gpt-5），按它匹配会把表拒给所有改了名字的人；按 `provider/id` 匹配则需要把 `config` 的解析规则搬进目录。裸 id 全表唯一这条约束已经把歧义挡在加载期。

## 4. clamp 的四条规则

`internal/catalog.Clamp(offered, want)`：

1. `off` 是开关不是档位，永不夹取，原样返回。
2. 模型有的档位原样返回。
3. 其余取规范序里最近的一档。两个档位等距（`medium` 在 `low` 与 `high` 之间）时取更低的，因为少花 token 是更小的错。比较用 rank，不看 `offered` 的顺序。
4. 认不出的名字退成 `off`。发一个不认识的级别会以适配器的默认值到达端点，那不是调用方要的；`off` 是唯一不会给人惊喜的答案。

**【pi】** pi 按模型的可用级别表夹取，规则相近。**【atlas】** 但塌缩留在适配器：DeepSeek 把 `medium`/`high`/`xhigh` 都翻成 `high`、把 `max`/`ultra` 都翻成 `max`，那张表在 `openai` 与 `anthropic` 两个适配器里各有一份，因为只有它们知道 dialect。目录只知道"这家端点文档里写着哪几档"，不知道"请求最终长什么样"。

## 5. 接线的五个面

`cmd/atlas/catalog.go` 是唯一的接缝：`factsOf` 做第 3 节的合并，`fitThinking` 做"校验 + 夹取 + 一句说明"。五个面都读同一个答案：

| 面 | 变化 |
|---|---|
| `modelOf` | 描述符里的窗口、输出上限、模态与显示名由表补齐；自动压缩因此对"只写 id"的配置也生效 |
| `-list-models` | 表和菜单打印的是生效值，不是声明值 |
| `/model` 菜单 | 每行的档位轴与 `ctx` 数字来自同一处 |
| `-thinking` | 词表内的档位夹到轴上并打 `warning:`；词表外的名字与"给不能思考的模型发档位"仍是错误 |
| `/model m:level` | 与 `-thinking` 同一条路径，夹过就在 `model:` 行下面多打一行 `warning:` |

**【atlas】** 为什么夹取而不是报错：菜单取走的位置本来就是轴上的一格，命令行写一个轴上没有的名字不该比它更严格；而且"发一个会被适配器压平的级别"比"夹到最近的一档并把这件事打出来"更糟——前者静默，后者在屏幕上。为什么拼错仍然报错：拼错不是偏好，`hgih` 夹出来只会是 `off`，那会把一次笔误变成一次"思考被关掉"。

## 6. 一条已知风险

一个自建端点上的模型恰好叫 `gpt-5`（不是那家厂商的 gpt-5）会拿到表里的窗口。这是按裸 id 查找的代价；改写一个 `contextWindow` 就能覆盖它，而反过来的设计（不查表）会让每一个配置重复抄厂商文档。

## 7. 验收

`tests/catalog/run.sh`（`internal/catalog`）：

- 七级词汇表与拒绝表（`HIGH`、`minimal`、带空格的名字都不算命中）。
- clamp 四条规则，含"等距取更低"与"与 `offered` 顺序无关"。
- `Gears` / `AllowsGear` 的三态。
- `Lookup` 只认精确 id；`All` 排序且改不动表。
- 九条种子逐字段；`Load` 的二十一条拒绝与规范化。

`tests/cli/run.sh`（`cmd/atlas`）：

- 只写 id 的模型拿到表里的窗口、输出上限、模态与窄轴；声明值逐项压过表；目录不打开 `reasoning`；不认识的 id 一个字段都不补。
- `-thinking`：`max`/`xhigh`/`ultra` 在 gpt-5 上都夹到 `high` 并有说明；`off` 对任何模型都放行；没有轴的模型与词表外的名字仍报错。
- `/model m:level` 的夹取与夹取说明；`-list-models` 打印表里的数字。

## 8. 不做的事

- 不做整份 models.dev 的生成器（`cmd/atlas-catalog`），也不在运行时访问网络。表的刷新是一次提交。
- 不在目录里编码适配器的塌缩表（第 4 节末）。
- 不做按 provider 的匹配与模糊匹配（第 3 节末）。
- RPC 那半在 0.12.0 接上了：表经 `app.Config.Models`（`[]app.ModelOption`）与 `app.Config.FitThinking` 钩子进内核，`get_available_models`、`cycle_model`、`cycle_thinking_level` 与 `set_thinking_level` 一起改读它。`get_session_stats` 在 0.12.2 接上，通过 `a.model.ContextWindow`（由 `cmd/atlas` 的 `modelOf` → `factsOf` 填入）换算 `contextUsage.percent`。保留的差异只有两处：内核没拿到表时（`-demo` 或前端自造一个 `app`）退回七级词表而不是报错，以及给不能思考的模型发档位在 RPC 仍报错（pi 静默夹到 `off`）。

## 9. 落地时改了什么

**0.10.1（叶子）**：新增 `internal/catalog`（`catalog.go` 158 行、`level.go` 108 行、`load.go` 174 行、`models.json` 93 行、`catalog_test.go` 342 行）与闸门 `tests/catalog/run.sh`。没有 import 任何 `pi-go/*` 包。

**0.11.0（接线）**：新增 `cmd/atlas/catalog.go`（`modelFacts` / `factsOf` / `fitThinking` / `levelValues` / `levelNames`）与 `cmd/atlas/catalog_test.go`；`modelOf`、`writeModelList`、`modelChoices` 改读 `factsOf`；`checkThinking` 被 `fitThinking` 取代（`runConfigured` 打 `warning:`），`resolveSpec` 多返回一个夹取说明并由 `models.apply` 打出；`models.switchModel` 的签名随之加一个返回值。`internal/config` 与 `internal/rpc` 一行未动——`config.Model.Gears()` 仍在那里，接线后的调用点不再直接用它，但它的校验与 `thinkingLevel` 的检查还在跑。

**0.12.0（RPC 接线）**：`internal/app`（686 行）新增 `ModelOption`（描述符加档位轴，空轴 = 这个模型不能思考，与"表里没有它"是两回事）、`ModelCycle`、`ThinkingFitter` 三个类型与 `Config.Models` / `Config.FitThinking` 两个字段，方法 `Models` / `AvailableThinkingLevels` / `FitThinking` / `CycleModel` / `CycleThinkingLevel` 都读同一个 `levelsOf`；`internal/rpc`（`rpc.go` 601 行、`command.go` 159 行）的 `unsupported` 从十二条减到九条，`get_available_models`、`cycle_model`、`cycle_thinking_level` 与改写过的 `set_thinking_level` 都走 `app`，无事可做时发 `data: null` 而不是省略键（pi 的 `success(id, cmd, null)`）；`cmd/atlas/catalog.go` 171 行，多两个纯函数 `modelTable`（把 `cfg.Selections()` 翻成 `[]app.ModelOption`，`Levels` 取 `factsOf(selection).gears`）与 `thinkingFitter`（把 `cfg.Lookup` 包成钩子，查不到就原样放行），`runConfigured` 把两者填进 `app.Config`。

夹取规则没有搬家：它仍在 `cmd/atlas` 的 `fitThinking` 里，`app` 只通过钩子调用它，因为 `catalog.Clamp` 是"按 rank 取最近、等距取低"，与"取不高于请求的最高档"不等价（gears `[off low max]`、want `high` 时前者给 `max`）。让 `internal/app` 直接 import `internal/catalog` 会同时破掉 `tests/app` 与 `tests/rpc` 的 `guard_deps_allowed`，而把 clamp 规则在 `app` 里再写一遍就等于有两个真相。

有一处实测推翻了我的估计：`off` 最初被放进"没有轴就报错"的分支里，而 `off` 对不能思考的模型也确实有意义（它是开关）。`TestRunRefusesAThinkingLevelTheModelDoesNotOffer` 当场把它抓住，`fitThinking` 因此先放行 `off` 再看轴。
