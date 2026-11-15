# /goal：目标模式

一个跨轮次的目标工作流：`/goal <objective>` 定一个目标，会话在目标驱动下连续工作（计划、改代码、跑检查），直到模型自己判定完成、读者打断、或花完 token 预算。状态随会话持久化，`/goal resume` 之后接着干。

依据：Codex 的 Goal Mode（CLI 0.128.0 首发，2026-05-21 转正默认开启，实现见 openai/codex #18073–#18077）与 mcode 的「目标功能」。本文前七节写在动手之前，第八节记着落地时改掉的几处。

## 1. 与 /plan、/resume 的分界

- `/plan` 是一段带文件的思考，产出是一份计划文本；`/goal` 是「把这件事干完」，产出是做完的活。
- `/resume` 是「回到上一次没说完的话」；`/goal` 是「回到那个没干完的目标」——状态是持久化的，跨进程重启也有效。
- codex 的目标不是操作系统级 daemon：状态随 thread 持久化，续跑由 CLI 在空闲时调度。atlas 同样没有后台进程，续跑由界面层调度（见第 5 节）。

## 2. 状态与持久化

goal 状态是会话文件里的一条 `custom` 条目：

```json
{"type":"custom","customType":"goal","data":{
  "objective":"…","status":"active","tokenBudget":100000,
  "tokensUsed":0,"turns":0,"created":"…","updated":"…"}}
```

- append-only：每次变化追加一条新条目，最新一条是当前状态（沿活跃分支倒序取第一条 `customType:"goal"`）。旧条目留在文件里，可查、可审计，且天然防止陈旧状态覆盖新状态。
- fork 自带分支起点时的目标：条目在树里，不在 App 里。
- 扩展状态不进上下文（`AppendCustomEntry` 的语义），所以 goal 不占模型的上下文窗口。
- `clear` 同样只追加一条 `status:"cleared"`，不删历史。

`status` 四态：`active`（循环运行，或等着读者下一条消息续跑）、`paused`（轮次失败/读者打断，`/goal resume` 继续）、`budget_limited`（花完预算，软停）、`cleared`（已清除，读作没有目标）。

## 3. 命令族

- `/goal`：摘要——目标、状态、轮数、token，有预算时带上已用/上限。
- `/goal <objective>`：设目标并开循环。已有目标时经选择器确认替换（无选择器时报错，让 `/goal clear` 先清）。
- `/goal pause`：停循环，保留目标。
- `/goal resume`：`paused`/`budget_limited` → `active`，并以 steering 提示开循环。
- `/goal clear`：追加 `cleared`。

与 codex 一致的两处取舍：目标一旦存在，直接输第二个 objective 不静默覆盖；`clear` 之后会话读作「没有目标」。

## 4. 循环规则（每轮结束后按序判定）

1. 轮次出错（含读者打断）→ 自动 `paused` 并返回错误。打断不丢目标，这是与「停下」的区别。
2. 记账：`tokensUsed += 本轮 Usage.TotalTokens`，`turns++`，追加新状态条目。
3. 预算：`tokenBudget > 0` 且 `tokensUsed >= tokenBudget` → `budget_limited` + 通知 + 停。软停不是失败：目标还在，`/goal resume` 继续（codex 的语义）。
4. 本轮没有工具调用结果（模型没动手就答了）→ 停循环但保持 `active`。模型要么干完了，要么在等读者输入；读者下一轮消息会把循环接起来（codex 的「用户输入优先」）。
5. 否则通知进度（`goal: N turns, X tokens; continuing`），以 steering 用户消息继续下一轮。

steering 提示（`Continue/Resume working toward the goal.`）用 `RoleUser` 而非 `RoleSystem`：进 transcript、可见可查，也不会被 persist 跳过。

完成判定是提示词里的一句话，不是工具：设目标时把「干完就停下来，总结成果与证据」写进首轮提示。这是 MVP 与 codex 最大的功能差（codex 有受限的 `update_goal` 工具与独立 verifier），见第 7 节。

## 5. 调度（loop 归界面层）

`internal/app` 的 `Ask` 契约不变：一次调用一轮，与 goal 无关。循环由 `cmd/atlas` 调度：

- `/goal <objective>` 与 `/goal resume` 在 `replCommand` 给的 `turn` runner（TUI 里是 `tuiTurn` 包装）内部执行 `goalLoop`，所以每轮的流照常进 answer block、Ctrl+C 照常打断。
- 接续 hook：`tuiMainTurn` 在读者主对话轮成功后，若 goal 仍 `active`，逐轮跑 `goalLoop`。这实现了「模型停下来等输入 → 读者回答 → 目标继续干」的闭环。
- 为让循环读到每轮的 `Result`（记账与判定工具调用都要），`turnRunner` 与 `tuiTurn` 的签名改为返回 `(app.Result, error)`。这是对 `/continue` 原有契约的一处小改。

MVP 边界：仅 TUI 有循环（单次 `-prompt` 与管道没有命令表，天然没有 `/goal`；测试直调 `replCommand` 时 `turn` 为 nil，`/goal <objective>` 与 `/goal resume` 干净报错）。

## 6. 预算来源

`config.json` 新增可选块：

```json
{"goal": {"tokenBudget": 100000}}
```

`0` 或整块省略 = 无预算。预算在设目标那一刻快照进状态（`app.Config.GoalBudget` 传入，`App.GoalBudget()` 读出），之后改配置不影响已存在的目标——与 codex 一致，一个目标的预算在该目标创建时定死。

## 7. MVP 边界（与 codex / mcode 的差距）

- 没有受限的 `update_goal` / `create_goal` 工具：模型不能自己宣布「完成」或改状态，只能靠提示词约定。
- 没有独立 verifier：不验证「目标真的达成了」，完成总结靠模型自觉。
- 没有 request limit 与时间预算，只有 token 预算。
- footer 不显示目标状态（摘要走 `/goal`，进度走每轮通知）。
- 没有桌面通知抑制这类平台细节。

## 8. 落地记录（写代码时改掉的）

1. **turnRunner 返回值**：原设计里 loop 经 `turn` runner 只能拿 error，拿不到 `Result`，记账与「有无工具调用」都做不了。改为 `turnRunner`/`tuiTurn` 返回 `(app.Result, error)`，`/continue` 调用点与 `continue_test.go` 的闭包跟着改。
2. **预算判定用 `>=`**：设计稿写「用超才停」，实现按「到达即停」——预算是个软上限，正好花到上限就该停，多跑一轮只会超出去。
3. **`/goal pause` 对非 active 状态**：不再报错，改报当前状态并提示 `/goal resume`——读者想要的是「停一下」，不是研究状态机。
4. **空 objective 不可达**：`strings.Fields` 已把 `/goal   ` 变成无参数（走摘要），`goalSet` 里的空串检查是防御性代码。
5. **example 模板**：goal 块进 `config.example.json`（可选项的样板），不进 `config.json` 模板（模板是「能跑的最小集」）。
