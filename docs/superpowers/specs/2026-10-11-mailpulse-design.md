# MailPulse 邮箱通知聚合服务设计

日期：2026-10-11。目标版本：v0.1.0。状态：供用户审阅的设计，项目名称为建议名称。

## 目标与已确认范围

一个可自部署的轻量服务同时监听多个 Gmail、Outlook 个人邮箱和 QQ 邮箱，把新邮件通知发布到现有 ntfy，再通过已有转发器送达 wxpusher。邮箱继续由原服务商保存；服务只读取通知所需的邮件元数据。

用户已确认 Go、SQLite、CLI、Docker 和后续 Web UI 的方向。第一版默认监听每个账户的收件箱，默认通知包含账户别名、发件人、主题和接收时间。可以隐藏主题或发件人，不获取正文、附件，不发送邮件，不删除邮件，不改变已读状态。

Microsoft 365 组织邮箱已确认能够正常收发邮件，但其管理中心问题和组织账户接入不属于首版验收范围。Graph 适配器保留 authority 参数，供后续组织账户接入使用。

首版不实现 Web UI、公网管理 REST API、Graph Webhook、Gmail API、AI 摘要或批量通知合并。通知合并放在后续版本；首版先验证每封邮件的可靠处理。

## 项目名称与实现方案

建议项目名 MailPulse，仓库名 mailpulse，命令名 mailpulse。名称表示邮件到达提醒，不代表名称已完成商标或重名检查。GitHub 仓库所属账户和公开状态在实际创建时沿用用户指定的目标，不凭空创建外部项目。

| 方案 | 优点 | 成本与限制 |
| --- | --- | --- |
| Go 模块化单体，推荐 | 一个容器，统一账户状态、规则和重试；可直接复用到 Web UI | 需要实现并测试 IMAP 和 Graph 两个适配器 |
| 现有 IMAP 监听器加脚本 | IMAP 接入起步快 | Outlook Graph、可靠队列和未来 UI 仍需独立开发 |
| Python 服务 | 邮件和 HTTP 原型容易实现 | 额外运行环境，与已选定的 Go 方向不一致 |

推荐方案采用标准库 net/http、log/slog、database/sql；SQLite 选用 modernc.org/sqlite，避免 CGo；IMAP 选用 emersion/go-imap/v2，并对其预发布版本风险进行接口隔离。微软授权优先使用官方 MSAL Go；Graph 使用小范围 HTTP 客户端，不引入整个 Graph SDK。实现开始时核对并固定兼容版本，提交 go.sum。

## 邮箱接入

| 服务商 | 协议和认证 | 首版策略 |
| --- | --- | --- |
| Gmail | TLS IMAP，imap.gmail.com:993，应用专用密码 | IDLE 加每 60 秒补查；账户需允许创建应用专用密码 |
| QQ | TLS IMAP，imap.qq.com:993，邮箱授权码 | IDLE 加每 60 秒补查；需开启 IMAP |
| Outlook 个人邮箱 | Graph v1.0，OAuth2 委托授权 | 设备代码授权，默认每 60 秒 Delta 增量查询 |

IMAP 不支持 IDLE 时采用轮询。保持只读选择邮箱，使用不会置已读的元数据读取操作；不依赖 UNSEEN，因为邮件可能已被其他客户端读过。IDLE 事件只触发补查，邮件 UID 才是处理依据。定期结束 IDLE 并重新进入，采用带抖动的重连退避。

Outlook 应用注册支持组织账户和个人微软账户，启用公共客户端流程；首版使用 consumers authority。每个账户独立授权和保存令牌；只申请 Mail.ReadBasic 和 offline_access，不申请写邮件权限。设备代码只在用户显式发起授权时获取，遵守微软返回的轮询间隔和过期时间。令牌刷新按账户串行执行，更新后的缓存持久化成功后才切换有效状态。

Graph 仅选择 id、subject、from、receivedDateTime、webLink 等基本字段。每个收件箱维护独立 Delta 状态，初次完成全部分页作为基线，后续关注新增项；处理 @removed 和仅状态变化条目时不发新邮件通知。所有相关请求携带 Prefer: IdType="ImmutableId"；逐页使用服务端返回的完整 nextLink/deltaLink，校验 HTTPS 和 Graph 主机，禁止把授权头发往其他主机。

## 模块边界与未来 Web UI

依赖方向为入口到应用服务，再到领域模型和端口接口；IMAP、Graph、SQLite、ntfy 实现端口。邮箱适配器不直接调用 ntfy，CLI 不直接执行账户管理 SQL。

| 模块 | 职责 |
| --- | --- |
| domain | Account、Rule、MailEvent、Delivery 等模型，错误分类 |
| app | 账户管理、授权、规则管理、状态查询、邮件处理与通知测试 |
| providers/imap | 基线建立、UID 增量扫描、IDLE、断线重连 |
| providers/graph | OAuth2、Delta 分页、限流和令牌刷新 |
| store/sqlite | 迁移、账户与规则、游标、去重记录、通知队列 |
| notify/ntfy | 标准 JSON 发布、响应分类、限流与重试 |
| runtime | 账户工作器调度、配置重载、停止与本地控制通道 |
| transport | CLI、本地控制通道、HTTP 健康检查 |

应用服务采用明确的输入输出结构和稳定账户 ID，不把 HTTP 请求或 CLI flag 传入领域层。未来 REST API、Web UI 调用这些服务；未来才确定 UI 框架、会话认证和 CSRF 防护，不提前安装前端依赖。

运行中的守护进程独占数据库写入。管理 CLI 通过权限受控的 Unix socket 调用守护进程，避免在多个进程中各自读写 SQLite 和刷新令牌。Docker exec 可以执行同一个二进制进行管理。停机初始化命令获取独占锁后执行；服务启动时检测重复实例。

## 持久化与通知可靠性

SQLite 是账户、规则、同步状态和发送队列的唯一事实来源。启动参数或配置文件只控制数据库路径、监听地址、日志和密钥文件；可通过 CLI 导入账户和规则，不用文件与数据库同时管理同一份账户设置。

建议表为 accounts、rules、sync_state、processed_events、outbox 和 schema_migrations。秘密不出现在普通账户查询结果里。队列只保存通知所需元数据，成功发送 7 天后删除其内容；未发送记录不自动过期。去重键和基线成员标识保留至账户删除或显式重置，不永久保留邮件主题。

每批增量结果按以下顺序处理：解析和规范化元数据、生成事件标识、执行规则，然后在一个数据库事务内写入去重记录和待发通知并更新同步游标。被过滤的事件也记录处理结果。提交成功后发送器才发送。分页之间可独立提交事件和下一页位置，失败重读同一页不会重复入队。

IMAP 事件键为账户 ID、文件夹、UIDVALIDITY、UID；Graph 使用账户 ID 和区分大小写的 Immutable ID。Graph 的首次基线保存已有 ID，但不创建通知，用来识别后续重复返回和移出后再移入的旧邮件。首次进入收件箱且此前未见过的邮件，包括手动移入的邮件，按新增项处理；该行为在 README 明确说明。

初次启用账户时建立基线，不推送历史邮件；基线完成后账户进入 running。首次基线建立期间的邮件归入基线，不保证推送；账户状态明确显示 initializing，避免把初始化完成前视作已经开始监听。已有有效游标的重启或断线恢复继续补查，不重新跳过积压邮件。只能通知仍可从服务商查询到的新增项。

ntfy 返回成功后标记 sent；网络故障、5xx 和 429 重试，遵守 Retry-After；401/403 标记发送配置异常并等待修正，其他永久错误进入 failed 状态，可通过 CLI 查看和重新入队。退避从 5 秒到 5 分钟，加入抖动；不能因为单个账户或单条失败记录阻塞其他账户。

投递采用至少一次语义：ntfy 已接收消息但本地来不及记录成功时，重启后可能重复发出。常规重连、分页重放和正常重启去重，不宣称端到端绝对只通知一次。ntfy 接收成功表示发布成功，不等于 wxpusher 或手机已经送达。

UIDVALIDITY 变化或 Graph 游标失效时，账户进入 needs_resync，保留待发通知并报告原因。首版不自动把所有邮件当成新邮件，也不静默跳过历史重新开始；用户通过 account resync --ignore-history 显式建立新基线，该操作会放弃失效游标期间无法确定的增量。恢复策略在状态输出和文档中明确说明。

队列达到容量限制或磁盘写入失败时停止推进游标，进入 degraded 并报告积压；首版不通过丢弃通知维持表面正常。队列上限默认 10000 条，可配置。SIGTERM 停止接收新批次，取消外部请求，完成进行中的数据库事务并回收发送中的记录，保证重启可恢复。

## 规则与通知内容

规则按顺序求值，首个匹配规则决定忽略、通知、优先级和隐私模式，无匹配时默认通知。条件覆盖账户 ID、发件人地址、发件人域名和主题包含关键词；同一规则中的条件为 AND。首版不提供正则和任意脚本。

通知使用 ntfy JSON 接口，便于中文标题。优先级允许 1 至 5，默认 3。正文用纯文本，标题如“新邮件 · Gmail 工作”，正文显示发件人、主题和接收时间。点击链接来自受信任的服务商网页或 Graph webLink，并限定 HTTPS；不自动打开邮件内容中的链接。

## 凭据与运行状态

账户凭据和微软令牌缓存采用 AES-GCM 加密保存在 SQLite，主密钥通过独立只读文件或 Docker Secret 注入，不和数据库放在同一备份中。文件权限限制为服务用户；密钥缺失或不匹配时启动失败，不生成新密钥覆盖现有凭据。首版不提供在线密钥轮换。

CLI 密码输入通过终端隐藏输入或秘密文件，禁止在命令行参数中直接传密码。Token、授权码、完整 OAuth URL、邮件主题和发件人不输出到默认日志；授权过程中仅向发起授权的 CLI 返回必要的设备验证码。日志使用账户 ID、错误类别、耗时和计数，并清理上游错误响应。

/healthz 检查主进程和数据库基本可用性；账户授权失效不使整个容器不断重启。/readyz 表示调度器和发送器能工作，允许部分账户处于 degraded；CLI status 显示逐账户状态、最近成功检查时间、待发数量和错误类别。状态至少包含 initializing、running、backoff、auth_required、needs_resync、paused。

每个账户独立工作器，串行同步；共享通知发送器采用有界并发，最大并发默认 4。默认 HTTP 健康检查监听容器内 8080，Compose 不发布端口。未来管理 HTTP API 需要单独启用和认证，健康检查不是管理接口。

## 仓库结构

以下是拟采用的目录结构，设计阶段仅交付本设计文档。

| 路径 | 内容 |
| --- | --- |
| cmd/mailpulse/ | 程序入口 |
| internal/domain/ | 模型、端口接口、错误类别 |
| internal/app/ | 应用服务与事件处理 |
| internal/providers/imap/ | 通用 IMAP 适配器及服务商预设 |
| internal/providers/graph/ | Graph 和微软授权 |
| internal/store/sqlite/ | 仓储与迁移 |
| internal/notify/ntfy/ | 通知发布 |
| internal/runtime/ | 调度、生命周期、本地管理通道 |
| internal/transport/cli/ | CLI |
| internal/transport/control/ | 本地控制协议 |
| internal/transport/http/ | 健康检查 |
| configs/ | 无真实凭据的示例配置 |
| deploy/ | Compose 示例和部署说明 |
| docs/ | 账户接入、架构、故障排查和本设计 |
| .github/workflows/ | CI、镜像构建与发布检查 |

建议 CLI 命令为 serve、init、account add/list/pause/resume/remove、account authorize、account resync、rule add/list/remove、notify test、outbox list/retry 和 status。账户移除采用显式确认并说明相关状态及待发记录的处理；首版暂停账户不删除任何状态。

## Docker 与交付

多阶段构建单一 Go 二进制；运行镜像包含 CA 证书和健康检查所需能力，以非 root UID 10001 运行。支持指定 user，但数据目录和秘密文件须对指定 UID 可访问。镜像只含程序及必需运行资产，不含编译器和源码。

首版镜像目标 linux/amd64 和 linux/arm64，发布到 GHCR。数据库和 socket 保存在可写数据挂载内，根文件系统可以只读。Compose 支持 init、重启策略和优雅退出超时。镜像大小与内存占用以实际构建和真实账户测试结果报告，不预先给出保证。

CI 运行 gofmt 检查、go vet、go test、race 测试、构建和 govulncheck；发布流水线构建多架构镜像并执行容器扫描。拉取请求不发布镜像。示例和测试使用虚构账户、假 Token 和本地模拟服务器，测试无需真实邮箱秘密。

## 验收与实现顺序

先完成领域模型、SQLite 和可靠发送队列，再接入 IMAP 与 ntfy，随后实现微软授权与 Graph，最后完成多账户 CLI、部署文档和发布检查。详细实现任务在设计审阅后编写。

自动化测试必须覆盖重复事件、事务失败、发送超时、重启恢复、规则过滤、Graph 多页断点、token 刷新失败、429、UIDVALIDITY 变化和游标失效；模拟 IMAP 服务器需验证不设置已读以及 IDLE 断线补查。每个失败用例需验证其游标和队列结果，不能只检查函数返回成功。

真机验收在用户自己的部署环境完成：三个服务商各至少一个账户，首次启动不推历史，后续新邮件能抵达手机，重启不重复正常已处理邮件，ntfy 暂停后恢复能补发，一个账号认证失效时其他账户继续工作。发布报告区分自动化通过与真机验证，不以模拟测试代替真实接入。

## 官方接口依据

- [Microsoft Graph message delta](https://learn.microsoft.com/en-us/graph/api/message-delta)：个人和组织账户均支持 Mail.ReadBasic；增量分页和变化类型受其接口约束。
- [Microsoft 设备代码授权](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-device-code)：支持个人账户，通过另一个设备完成交互授权。
- [Graph Immutable ID](https://learn.microsoft.com/en-us/graph/outlook-immutable-id)：同一邮箱内跨文件夹移动保持消息 ID，需在每次相关请求中携带指定头。
- [ntfy 发布接口](https://docs.ntfy.sh/publish/)：JSON 发布、标题、优先级和点击链接。
- [go-imap 项目](https://github.com/emersion/go-imap)：IMAP 客户端与 IDLE 能力，适配器隔离具体库接口。