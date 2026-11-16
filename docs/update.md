# 检查更新与 /update

打开 atlas 时自动查一次 GitHub release：有新版，首页就多一行提示；下载与安装只在敲 `/update` 之后发生，装完重启生效。

发布通道是公开仓库 `youverus/atlas`（只放 go 实现的快照，从 0.12.23 开始发布；0.12.22 及更早留在私有仓库 youverus/pi），所以检查与下载可以全匿名——零 token、零 ssh 依赖，服务器场景可用。0.12.23–0.12.24 检查走 GitHub API，0.12.25 起改走网页层（API 对匿名流量按 IP 限流，共享出口 IP 报 403）。本文前七节写在动手之前，第八节记着落地时改掉的几处。

## 1. 检查与安装是两件事

- 检查是"看"：打开 atlas 时一次匿名 GET，有新版只在首页说一句。它不改任何文件。
- 安装是"动手"：只有 `/update` 会下载、校验、替换二进制。检查找到的新版不自动装。
- 一条边界：`-prompt` 与管道模式不检查——那里没有命令表，也就没有 `/update`，报一句"有新版"是空头支票。

## 2. 发布通道（公开仓库 + 匿名网页层）

- 检查（0.12.25 起）：`GET https://github.com/<repo>/releases/latest`，302 的 Location 指向 release 页、tag 就在路径里，`CheckRedirect` 取出来即停——一次请求即得 tag，10 秒超时。此前走 `api.github.com/repos/<repo>/releases/latest`，但 API 对匿名流量按 IP 限流（60 次/小时），共享出口 IP 在第一次检查前就烧完配额、报 403 `API rate limit exceeded`；网页重定向没有这条配额。仓库没有 release 时该 URL 302 到 `/releases`，Location 里没有 tag，报 `GitHub has no release to follow`。
- 下载：资产 `atlas-<GOOS>-<GOARCH>.tar.gz` 与 `SHA256SUMS` 同走匿名 URL，由 tag 推导（`releases/download/<tag>/…`；发布管线命名即约定，不需要资产清单，缺资产由下载 404 拒绝）。
- repo 缺省 `youverus/atlas`，`config.json` 的 `"update": {"repo": "owner/name"}` 可覆盖（读者发布自己 fork 的场景）；配置在进程开始时读一次，会话中途改配置下次打开生效。
- tag 规则：`atlas-vX.Y.Z`。`parseAtlasVersion` 剥前缀、逐段校验数字；`compareVersions` 逐段比数字，非数字段回落字符串序（pre-release 后缀不会被当数字段读错）。

## 3. 启动检查（失败静默）

- 时机：TUI 的 program 建好之后另开 goroutine 检查，不挡第一帧。
- 结果：有新版 → 首页 Flash `atlas X is available — /update to install`；没有新版、查不到、超时 → 一个字不说。检查是便利，不是承诺。
- 为什么是一行 Flash 而不是弹窗：打开会话要立刻能用；Flash 在首页上，`/update` 就在命令面板里，一行就能接上。

## 4. /update（两道确认 + 校验 + 原子替换）

1. 拒绝 windows（`self-update is not supported on windows`）与拿不到可执行路径。
2. 查最新版：不比当前新就只报 `atlas X is the newest release`。
3. 有菜单时问 `update atlas` 的 upgrade / keep，关闭或 keep → `staying on atlas X; /update runs the upgrade`；`pick == nil`（管道、测试）视为已经同意。
4. `updating atlas X -> Y`：资产下载到 exe 同目录的临时文件 `.atlas-update-*.tar.gz`，再下载 `SHA256SUMS`、取该资产的 sha256 比对（大小写不敏感），任何一步失败都不碰正在跑的二进制。
5. 解包出二进制写到 `.atlas-next-<pid>`（0755），`os.Rename` 原子替换——中途崩溃只会留下旧文件或新文件，不会留下半份；macOS 上替换正在执行的二进制合法，旧 inode 由内核留到进程退出。
6. 收尾 `updated to atlas X; restart atlas to run it`。

不自动重启：替自己重启要处理会话状态、终端的接管与"进程自己换了文件"的边界，收益只有省一次退出重开。

## 5. 配置

`config.json` 可选块：

```json
{"update": {"repo": "youverus/atlas"}}
```

- 缺省 = 内置 `youverus/atlas`；`Validate` 要求 `owner/name` 形状，写错以 `update:` 前缀报错。
- example 样板带这个块；`config.json` 模板不带（模板是"能跑的最小集"，缺省已可用）。

## 6. 校验为什么在这里更要紧

匿名下载意味着任何网络位置都可能插一手；`SHA256SUMS` 随 release 一起发布，下载包先过校验再过 rename——校验不过就地拒绝。诚实的边界：校验和与包同源，防的是传输损坏与浅层篡改，不是供应链攻击；签名信任链不做，见第 7 节。

## 7. MVP 边界

- 不自动下载、不自动重启（第 4 节）。
- windows 上 `/update` 拒绝；启动检查照跑（首页照提示，命令会拒绝——跨平台的替换是另一件事）。
- 检查只在 TUI；`-prompt` 与管道不查。
- 校验和与包同源，签名信任链不做。
- 私有仓库不支持：404 即静默（公开仓库是整个设计的前提）。
- 更新源一个进程只读一次，`/reload` 不重读。

## 8. 落地记录（写代码时改掉的）

1. **tar entry 名**：真实发布包里的 entry 是裸文件名 `atlas-<os>-<arch>`，测试脚手架一开始误写成带 `.tar.gz` 后缀，`extractBinary` 按名字找不到——测试改用 `strings.TrimSuffix` 与发布形态对齐。
2. **`program.Send` 与退出竞态**：bubbletea v2 的 `Program.Send` 在程序退出后是 no-op（查过源码），检查 goroutine 再用 `context.WithTimeout(ctx, updateTimeout)` 兜底，程序退出后检查结果不会写坏任何东西。
3. **注入做成包级 var 而不是参数**：`replCommand` 的签名已经很长，而测试要换的是整个 HTTP 客户端与二进制落点——`updateClient` / `updateExecutable` 两个 var 加 `t.Cleanup` 还原，代价是测试不能 `t.Parallel`（cmd/atlas 本来就没有并行用例）。
4. **版本比较不手写溢出逻辑**：`strconv.Atoi` 出错即整串回落 `strings.Compare`，一个"畸形但更长"的版本不可能比较成相等。
5. **响应体上限**：`downloadText` 限 1MiB（`io.LimitReader`），坏服务器撑不爆内存。0.12.25 起 `latestRelease` 根本不读响应体（tag 从 302 的 Location 取，`http.ErrUseLastResponse` 停住跟随），这条上限只留给 `SHA256SUMS` 的下载。
6. **检查改走网页重定向（0.12.25）**：API 的匿名配额按 IP 算（60 次/小时），服务器与办公机共用出口 IP，配额耗尽即 403——而启动检查失败静默，403 就表现为「不提示」。网页层的 `/releases/latest` 302 没有这条配额，tag 在 Location 路径里，`CheckRedirect` 捕获即停；资产 URL 由 tag 推导，`releaseInfo` 收成只剩 `TagName`，删掉 API 用的 JSON 解码与资产清单类型。
