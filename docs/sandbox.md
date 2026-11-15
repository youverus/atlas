# 沙箱：抄什么、不抄什么

这份文档有两半。前七节是动手前的依据：把 `@anthropic-ai/sandbox-runtime`（下称 srt）的实现抄到能落地的粒度，并标出哪些不抄。第 8 节是 0.6.0 落地的范围与验收，第 10 节记下实现时改了什么、为什么改——**沙箱在 0.6.0 之前不存在**，那之前 `bash` 和 `git` 一旦被允许就拥有 atlas 进程的全部权限。

实现落在 `go/internal/sandbox`（policy、SBPL 生成、macOS 执行器）与 `go/internal/tool/shell`（那个两行的 `Sandbox` 接口）。本文件第 3 节的 profile 骨架就是 `profile.go` 里 `processRules` 与 `deviceRules` 的原文。

参考实现有两处，都在这台机器上可查：

- srt 本身：Anthropic 的 `sandbox-runtime`，Claude Code 的沙箱层。
- MiniMax Code（`@minimax-ai/code`）把它 vendored 进打包文件，并加了一层 MITM CA。本文的代码形状主要引自这份打包产物；它随版本搬过家（0.6.12 是 `chunks/chunk-5ADYFF2U.js`，0.7.6 是 `chunks/chunk-6TRFTSP4.js`），而权限那半在 `chunks/chunk-53I2XNVN.js`，见 [`permissions.md`](permissions.md)。

标注约定：**【srt】** 表示从参考实现读到的形状；**【实测】** 表示在本机（macOS 27.0.1 / arm64）跑出来的结果；**【atlas】** 表示我们自己的决定。

## 1. 责任边界

执行器必须是内核的。用户态的"沙箱"只能约束听话的程序，而 agent 恰恰会产生不听话的命令：`bash` 里一条 `/usr/bin/curl` 不经过我们的代码，一个静态链接的二进制直接 syscall 也绕过。

所以"自己实现"指的是三件事：

1. 定义 policy 结构（可写根、只读路径、网络开关）；
2. 把 policy 编译成平台策略；
3. 决定在哪一层套上。

执行交给内核。这不是妥协——srt 和 MiniMax Code 都这么做。

## 2. 两层的分工

| 层 | 管什么 | 谁执行 | 现状 |
|---|---|---|---|
| L1 规则表 | 哪个工具、哪条命令、哪个路径 | 我们自己 | 只有侧会话的按工具名允许名单 |
| L2 内核 | 被包裹进程树能碰什么 | Seatbelt / bwrap / seccomp | 无 |

两层不能互相替代：

- 只有 L1：`bash` 一旦放行，文件系统边界就没了。
- 只有 L2：每一步都要人点同意，沙箱变成一个更慢的 bash。

参考实现把两者接在一处，见第 6 节的 `autoAllowBashIfSandboxed`。

## 3. macOS：Seatbelt

**【srt】** 用 `sandbox-exec -p <profile>` 包住 `bash -c`，profile 由代码生成。骨架是：

```
(version 1)
(deny default (with message "<logTag>"))

; LogTag: <logTag>

; Essential permissions - based on Chrome sandbox policy
; Process permissions
(allow process-exec)
(allow process-fork)
(allow process-info* (target same-sandbox))
(allow signal (target same-sandbox))
(allow mach-priv-task-port (target same-sandbox))

; User preferences
(allow user-preference-read)

; Mach IPC - specific services only (no wildcard)
```

三处值得注意：

- `(deny default ...)` 是白名单起点，不是黑名单。所以每一条能力都要显式给。
- 必备权限里 `signal` 与 `process-info*` 都带 `(target same-sandbox)`——只管自己这棵树，管不到宿主。
- Mach IPC 必须按服务名逐个给，通配符会让隔离形同虚设。

读权限：写 `(allow file-read* (literal "/"))` 当"允许读全部"；有 deny 区域时另加 `(allow file-read-metadata (vnode-type DIRECTORY))`，否则被 deny 的目录连"它存在"都看不到，`ls` 会报错而不是报空。

写权限分三档：

| 档 | 生成规则 | 用途 |
|---|---|---|
| `allowOnly` | `(allow file-write* (subpath …))` 逐条 | 只许写这些地方 |
| `denyWithinAllow` | 在上面之后 `(deny file-write* (subpath …))` | 允许的根里挖掉危险点（`.git/hooks`） |
| `unlinkAllowOnly` | 先 `(deny file-write-unlink (subpath "/"))` 再逐条 allow | 连删除也单独收窄 |

**顺序有意义**：Seatbelt 是后写的规则覆盖先写的，所以"先宽后窄"才能挖洞。

**【实测】** 本机踩到的四个坑，抄的时候必须一起抄：

| 现象 | 原因 | 处理 |
|---|---|---|
| 写 `/tmp/x` 全被拒，即使 profile 允许 | `/tmp` 是符号链接，`subpath` 匹配的是解析后的路径 | 规则里写 `/private/tmp/...`，生成前先 `realpath` |
| `git status` 报 `could not open '/dev/null'` | `deny file-write*` 把设备文件也拒了 | 显式放通 `/dev/null`、`/dev/tty`、`/dev/stdout`、`/dev/stderr`、`/dev/zero` |
| 禁网后 `curl` 报的是 DNS 失败 | 解析域名本身就要网络 | 这不是 bug，是"网络全禁"的正确表现 |
| 在已沙箱化的进程里再跑 `sandbox-exec` → `sandbox_apply: Operation not permitted` | 一个进程树只能套一次 Seatbelt | 不能"外层放网、内层断网"叠加；见第 7 节 |

第四条是硬约束，直接决定了架构：**要么整进程被约束，要么只包子进程，不能两层叠加**。

### 网络：Seatbelt 做不到域名

**【实测】** 写 `(remote ip "127.0.0.1:8899")` 报 `host must be * or localhost in network address`。也就是说网络过滤只认 `*` 和 `localhost`，不认 IP、不认域名。

**【srt】** 的绕法：

1. profile 里 `(deny network*)`，只放通 `localhost:<port>`；
2. 沙箱**外**跑一个 HTTP/SOCKS 代理，监听本机 unix socket（MiniMax Code 里是 `mcode-sandbox-http-<rand>.sock` 与 `mcode-sandbox-socks-<rand>.sock`，带 `proxyAuthToken`）；
3. 沙箱内所有进程通过 `HTTP_PROXY` / `HTTPS_PROXY` 指过去；
4. 因为要按域名判，代理必须能看明文，于是**生成一个临时 CA**（目录名 `srt-ca-<rand>`，CN `sandbox-runtime ephemeral CA`，默认有效期 825 天）注入信任链；JVM 工具额外靠 `JAVA_TOOL_OPTIONS` 挂 `srt-proxy-agent.jar`。

**【实测】** 这套是可行的：`(deny network*)` + `(allow network* (remote ip "localhost:8899"))` 下，本机 8899 返回 200，外网连 DNS 都不通。

代价要写清：域名白名单是 MITM 出来的，不是内核策略。放行 `github.com` 就是整个域名放行，任何允许域名上的任意接口都成了外泄面。

## 4. Linux：bwrap + seccomp

**【srt】** 的 argv 形状（顺序以代码为准）：

```
bwrap
  --new-session --die-with-parent
  --unshare-user --cap-drop ALL --proc /proc
  --dev /dev --unshare-pid
  -- <shell> -c <command>
```

- `--die-with-parent` 保证 atlas 退出时不留孤儿。
- `--unshare-user --cap-drop ALL` 把 capability 清空。
- unix socket 不在 bwrap 的能力范围内，靠独立的 `apply-seccomp` 二进制过滤；没有它就打一条 warning：`seccomp not available - unix socket access not restricted`。
- 网络同样走 socat 代理，形状与 macOS 一致。
- mount point 有引用计数清理（`[Sandbox Linux] Deferring mount point cleanup — N sandbox(es) still active`），因为多个沙箱共享同一批挂载点。

**【atlas】** Linux 侧的第一版可以更窄：`bwrap` 是可执行文件依赖（和 `sandbox-exec` 一样是外部执行器），但要接受"没装 bwrap 就没有沙箱"这件事，并且**不能静默降级**——要像 MiniMax Code 那样报 `bubblewrap (bwrap) not installed`。

纯 Go 的 Landlock 路线（raw syscall 444/445/446 + `prctl(PR_SET_NO_NEW_PRIVS)`）不做第一版：Landlock 是 per-thread，必须放在独立进程里、下完规则再 `syscall.Exec`，等于要写一个 helper 二进制。等 bwrap 这条走通再评估。

## 5. Windows：不做

MiniMax Code 在 Windows 上不用进程沙箱，用 WFP（Windows Filtering Platform）的 sublayer——只做网络过滤，不做文件系统。**【atlas】** 第一版不支持 Windows 沙箱，`shell` 工具本身仍可编译到 Windows（`tests/shell/run.sh` 有 `GOOS=windows` 的交叉 vet），但沙箱只在 darwin 与 linux 上生效，其他平台明确报"不支持"而不是假装有。

## 6. L1：规则表与那个交换

**【srt】** 的规则形状：`{toolName, ruleBehavior: allow|deny|ask, ruleContent?}`。`ruleContent` 是内容匹配器，MiniMax Code 的 `permission.json` 里长这样：

```json
{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "curl:*"}}
```

默认行为：没有规则匹配就是 `ask`（`No local permission rule matched <tool>; approval is required`）。

六种模式，映射到审批策略：

| 模式 | 审批策略 | 含义 |
|---|---|---|
| `default` | on-request | 按规则问 |
| `acceptEdits` | on-request | 同上，编辑类放宽 |
| `auto` | on-request-llm | 让模型自己判断要不要问 |
| `dontAsk` | deny | 不问，直接拒 |
| `bypassPermissions` | never | 不问，直接放 |
| `off` | never | 等同关闭 |

**最关键的一条**：

```js
if (tool === "bash" && permission.sandboxEnabled && permission.autoAllowBashIfSandboxed)
  // Bash ask rule bypassed by sandbox
```

沙箱开着的时候，`bash` 的 ask 规则被跳过。

**【atlas】** 这是整套设计里最值得抄的一条，因为它把两层的成本对上了：沙箱不是"拦住模型"，而是"**让你敢把 bash 的审批关掉**"。只做沙箱不做这个交换，用户还是要一条条点同意，沙箱的收益为零。

它在参考实现里的位置是判定阶梯的第 4 步，只跳"整工具的 bash ask 规则"——内容级 ask 与 deny 都不受它影响。完整的阶梯（以及为什么"始终授权"排在所有 ask 之后）见 [`permissions.md`](permissions.md) 第 3 节。

## 7. 三个值得抄的细节

**一、强制 deny 列表在每次包裹时现场枚举。** 不是写死的字符串列表，而是跑一次

```
rg --files --hidden --max-depth <N> --iglob <patterns>
```

找出工作区里所有 `.git/hooks`、`.git/config`、shell rc 文件、`.mcp.json`、`.vscode` / `.idea`、`.claude/commands`、`.claude/agents`，再逐个 deny。理由是嵌套仓库：一个子目录里可能有自己的 `.git/hooks`，静态列表抓不到。`--max-depth` 默认 3，可配到 10，深度换性能。

**二、空的 `GIT_TEMPLATE_DIR`。** 包裹时建一个空目录并设为 `GIT_TEMPLATE_DIR`，防止沙箱里跑 `git init` / `git clone` 时装上 hooks。同一处还设了 `MAVIS_TRASH_FORCE_MV=1`，让删除走可恢复路径而不是真删。

**三、违规遥测。** macOS 上直接读系统日志：

```
log stream --predicate '(eventMessage ENDSWITH "<logTag>")'
```

把 Seatbelt 的 deny 记录归一化成 `read_denied` / `write_denied` / `delete_denied` / `network_denied` / `local_access_denied`，再按位置分类成 `workspace` / `git` / `runtime-data` / `session-temp` / `network` / `local-service` / `filesystem-other`，上限 100 条，另外发 `sandbox.violation` / `sandbox.violation_summary` 两个运行时事件。

意义是"沙箱拦了什么"可数，不是黑箱。没有它，用户看到的只有"命令失败了"。

## 8. 【atlas】第一版的范围

做（0.6.0 已落地）：

1. `internal/sandbox`：policy 结构与 SBPL 生成，不碰 syscall。
2. 执行器只实现 macOS（`/usr/bin/sandbox-exec`），Linux 留同一接口的"未实现"分支，报一句可行动的话。
3. `shell` 的子进程统一走它；将来的 `git` 子进程用同一个接口。
4. 干净 env：不继承 `GIT_*`，固定 `GIT_OPTIONAL_LOCKS=0`、`GIT_PAGER=cat`、`GIT_TERMINAL_PROMPT=0`、`LC_ALL=C`、空的 `GIT_TEMPLATE_DIR`。
5. 现场枚举 deny 列表（第 7 节第一条）。
6. 幂等标记 `ATLAS_SANDBOXED=1`。

不做：

1. 整进程 re-exec。srt 与 MiniMax Code 都只包子进程，代价（TUI、信号、exit code 透传）不抵收益。这也意味着 atlas 自己的 `write` / `edit` 仍在沙箱外——它们靠 L1 的路径策略，不靠内核。
2. 本地代理与 MITM CA。atlas 今天没有域名白名单的需求；`web` 工具走 DeepSeek 端点，`bash` 联网是允许的。等做"防外泄"时再加，那时才需要第 3 节那套。
3. Windows。
4. 违规遥测。第一版连"把 stderr 里的 `Operation not permitted` 归类"都没做：错误文本已经是内核给的，再包一层分类只是把同一句话说两遍。
5. `autoAllowBashIfSandboxed`（第 6 节那个交换）。**推迟的理由不是优先级，是没有消费者**：atlas 的主对话今天根本不问 bash（只有侧会话有按工具名的允许名单，`cmd/atlas/gate.go`），所以"沙箱开着就跳过 ask"没有可跳过的 ask，写下去就是死代码。等 L1 规则表那一片落地时再接，那时这个交换才有意义。L1 的形状现在有了：[`permissions.md`](permissions.md) 第 7 节把它列进第一版的范围（第 5 条），粒度按参考实现的第 4 步——只跳整工具 ask，不跳内容级 ask 与 deny。

验收（每一条都是一条会变红的测试，`tests/sandbox/run.sh` 跑它们）：

1. 沙箱内写 cwd 成功，写 cwd 的兄弟目录失败。→ `TestSeatbeltWriteInsideAndOutside`
2. `.git/hooks` 在可写根包含 cwd 时仍被拒，且错误读起来是内核拒的（含 `not permitted`）。→ `TestSeatbeltRefusesGitHooks`
3. `.git/config` 同样被拒。→ `TestSeatbeltRefusesGitConfig`
4. `/dev/null` 可写（否则 `git status` 直接跑不起来）。→ `TestSeatbeltAllowsDevNull`
5. 幂等标记存在时不重复包裹。→ `TestWrapIsIdempotent`
6. 沙箱不可用（没有 `sandbox-exec`、平台不支持）时报错而不是静默放行。→ `TestWrapRefusesWithoutExecutor`
7. **反向的那一条**：`git init` / `add` / `commit` / `status` 在沙箱里全都照常。→ `TestSeatbeltKeepsGitWorking`

第 7 条是 0.6.0 新增的，也是原计划里那条"`git commit` 必须失败"的替代品——理由见第 10 节。一个把提交也拒掉的沙箱不会让人更安全，只会让人把沙箱关掉。

## 9. 未解问题

1. **`sandbox-exec` 已弃用**（`man sandbox-exec` 第一行就是 `(DEPRECATED)`，2013 年至今）。0.6.0 把它收在一个函数后面（`defaultExecutor`，`executor_darwin.go`），Apple 哪天删了就换 `container` / VM：policy 与 SBPL 生成不动，只换那个返回路径的函数和它的渲染器。
2. **"只包子进程"的缺口**：atlas 的 `write` / `edit` 是进程内 `os.WriteFile`，不受 Seatbelt 约束。这是抄 srt 的代价，不是抄错了——但要写进文档，不能让人以为"开了沙箱就安全"。
3. **嵌套不可用**（第 3 节第四条）意味着"bash 断网、模型有网"在单进程里做不到。真要做到只有两条路：本地代理（第 3 节），或整进程进容器。
4. **`git` 工具的只读保证**有两层，缺一不可：argv 白名单负责"不给表达"，沙箱负责"给了也做不到"。只有前者时，`git diff` 的 `--ext-diff` / `--textconv` 仍能执行仓库配置里的任意命令。0.6.0 只落了第二层的一半（`bash` 里的 git），第一层还没写。
5. **读没有隔离。** profile 里是 `(allow file-read*)`：`~/.ssh`、别的项目、任何一份凭据在 `bash` 里都读得到。要收窄得先有一份"什么不该被读"的清单，而那份清单与第 7 节的"什么不该被写"不是一类东西——前者是秘密，后者是程序，不能靠同一个目录扫描得出。

## 10. 落地时改了什么

写代码的过程推翻了原计划的四处，每一处都有实测撑着。记在这里是因为第 1 到 7 节写的是"打算怎么做"，而下面这些是"实际能怎么做"。

**一、`git commit` 必须成功，不能失败。** 原验收第 2 条要求沙箱内 `git commit` 失败（`index.lock: Operation not permitted`），理由是那最能证明"边界在内核里"。实测之后发现这条要价太高：让它失败的唯一办法是把 `.git` 整个拒掉，而那样 `git add`、`git stash`、`git checkout` 也一起没了——一个不能提交的 `bash` 会让人直接把 `sandbox` 块删掉，那时什么边界都没有。真正需要的证据在 `.git/hooks` 与 `.git/config` 上，而它们同样在可写根里面：写不进去只可能来自内核。所以验收换成了"两个拒绝 + 一个照常"，两条拒绝证明边界，一条照常证明这个边界不是"什么都干不了"。

**二、`.git/hooks` 无条件拒，`.git/config` 只在已存在时拒。** 第 7 节列的是两者都拒，没区分。实测：`git init` 与 `git clone` 都要写 `.git/config`，无条件拒就是"沙箱一开就不能建仓库"。而 hooks 目录可以无条件拒，因为**空的 `GIT_TEMPLATE_DIR` 让 git 根本不去创建它**——第 7 节第二条把它记成"防 hooks 装上"，实际作用更强：它是那条无条件拒绝能存在的前提。代价写在 `profile.go` 的注释里：一条命令内同时建仓库并改写它的 config 不受保护，之后的每条命令都受。

**三、工作区级的那批名字只在根上拒。** 第 7 节说现场枚举 `.git/hooks`、`.git/config`、shell rc、`.mcp.json`、`.vscode` / `.idea`、`.claude/commands|agents`，听起来是一份清单扫到底。实现时分开成两类：**仓库**到处都拒（嵌套仓库有自己的 hooks，这正是现场枚举的理由），**工作区级配置**只在根上拒——四层目录下的 `.vscode` 是项目里的一个目录，不是编辑器的配置，而每层都生成十几条规则会让 profile 从 4KB 涨到几十 KB，扫一次也从毫秒变成十几毫秒。shell rc 同理：`$HOME` 本来就在可写根之外，那里的 `.zshrc` 已经被默认拒绝挡住了，工作区里的 `.zshrc` 没人会 source。

**四、扫描必须跳过 `node_modules`。** 第 7 节只说了 `--max-depth` 换性能，没提这件事。一个装了依赖的 JS 项目，深度 3 的扫描会走进上万个小目录，而那里的 `.git` 不是用户要操作的仓库。跳过 `.git` / `.vscode` / `.idea` 是顺带的：前三个整块被拒，进去只是重复父规则。

还有三处小的。**`Policy.Deny` 没有落地**：原计划让调用方补"扫描看不见的路径"，实现完发现没有调用方需要它（atlas 要拒的都在工作区里，扫描已经覆盖），一个没有消费者的字段只是让人猜它什么时候该用。**profile 用 `-p` 内联传，不落临时文件**：4KB 的 profile 远在 `ARG_MAX` 之内，落盘就得管清理，而 `Wrap` 不知道命令什么时候结束。**`$TMPDIR` 必须进可写根**：`go build` 要在那里建工作目录，不给就是 `go: creating work dir: mkdir ...: operation not permitted`——而这个错误会被读成"Go 装坏了"。它同时也是第 3 节那个符号链接坑的第二个例子：`$TMPDIR` 在 `/var/folders/...` 下，而 `/var` 是 `/private/var` 的符号链接。

### 0.8.0 补齐的三处

1. **读路径隔离（`Policy.DenyRead` 与 `sandbox.denyRead`）。** 第 9 节第 5 条留下的"读没有隔离"在 0.8.0 补上：`sandboxFor` 默认把 `~/.ssh`、`~/.aws`、`~/.kube`、`~/.gnupg` 放进 `DenyRead`，`config.json` 的 `sandbox.denyRead` 可追加路径。macOS Seatbelt 在 `(allow file-read*)` 之后逐条写 `(deny file-read* (subpath ...))`，并跟一条 `(allow file-read-metadata (vnode-type DIRECTORY))` 让父目录 `ls` 不报错；如果可写根落在某个被拒的父目录里（例如拒了父目录而工作区在里面），再补一条 `(allow file-read* (subpath <writable>))` 把工作区重新打开。尚不存在的 `DenyRead` 路径（如新机器还没有 `~/.ssh`）会先解析其已存在的父目录（`canonicalOrParent`），让 Seatbelt 在命令创建它之前就盯住真实路径。
2. **Linux `bwrap` 执行器（`executor_linux.go` 与 `state.bwrap`）。** 按第 4 节的 argv 形状落地：`--new-session --die-with-parent --unshare-user --cap-drop ALL --proc /proc --dev /dev --unshare-pid`，断网加 `--unshare-net`，然后按"先宽后窄"挂载：`--ro-bind / /` → 可写根 `--bind` → `DenyRead`（目录 `--tmpfs`、文件 `--ro-bind /dev/null`）→ 写保护区 `--ro-bind`（把 `.git/hooks` 等重新锁成只读）。找不到 `bwrap` 时报 `bubblewrap (bwrap) not installed` 而不静默降级。
3. **`autoAllowBashIfSandboxed` 接进 `internal/permission`。** 第 8 节不做第 5 条等待的消费者在 0.8.0 落地：当沙箱确实可用且 `permission.autoAllowBashIfSandboxed` 为真时，`permission.Engine` 跳过整工具的 `bash` ask，保留内容级 ask 与 deny（见 [`permissions.md`](permissions.md) 第 7、9 节）。
