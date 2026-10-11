# 运行与故障排查

## 状态

status 输出每个账户的稳定 ID、状态、最近成功检查时间、错误类别和 `pending_notifications`，并保留全局待发数。统计包含 pending、blocked、failed，排除 sent；即使账户还没有首次状态记录也会显示，未启用账户显示 paused。逐账户与全局计数来自同一数据库快照。不会输出密码、令牌、主题或发件人。

account list 显示账户配置。outbox list 使用游标分页，默认每页 100 条，`--limit` 可设为 1–1000；可按账户和状态筛选。返回格式由旧的数组调整为 `{ "items": [...], "next_cursor": "..." }`，没有下一页时省略 next_cursor；使用旧输出格式的脚本需要调整。任务只包含 ID、账户 ID、状态、尝试次数和错误类别，不显示消息内容。

```sh
mailpulse outbox list --limit 100 --status failed --account ACCOUNT_ID
mailpulse outbox list --limit 100 --status failed --account ACCOUNT_ID --after NEXT_CURSOR
```

第二次请求保持相同筛选条件，将上一次返回的 next_cursor 原样传给 --after。支持 pending、blocked、failed、sent；不指定状态则查询全部。分页按任务 ID 排序，期间有新增、发送或删除时，后续页反映当前数据，不是冻结的快照；实时变化后需要从第一页重新检查。发送器处理所有任务，不受单页大小限制。

- initializing：初次建立基线，尚未承诺新邮件通知。
- running：增量扫描正常。
- backoff：临时连接或服务商错误，自动重试。
- auth_required：需要修正凭据或重新微软授权；不会一直尝试坏密码。
- needs_resync：游标失效，需要显式重新建立基线。
- degraded：队列容量达到上限，保留游标等待积压减少。
- paused：账户已暂停扫描。

## ntfy 失败

网络问题、429 和 5xx 进入重试；遵守 Retry-After。401/403 标记 blocked，notify configure 更新配置后解除阻塞。其他永久错误为 failed，修正原因后用 outbox retry 重试。

notify test 直接测试 ntfy 发布。它不创建邮箱事件；测试成功仅表示 ntfy 接收请求。手机没有收到时继续检查订阅、主题权限及现有 ntfy 到 wxpusher 转发器。

## 健康检查与权限

/healthz 检查主进程和数据库可访问性，/readyz 检查调度器和发送器。个别账户失败不导致容器反复重启。

默认向 stderr 写 JSON 诊断日志，仅包含账户 ID、状态、错误类别、耗时和事件数。不会记录账户地址、别名、邮件主题/发件人、Token、密码或上游原始错误。运行配置 `log_level` 或环境变量 `MAILPULSE_LOG_LEVEL` 可设为 debug/info/warn/error，默认 info。存储提交失败标记 degraded，不推进游标；若数据库无法保存状态，日志仍报告 storage_unavailable。

SIGTERM 会取消外部请求，并用独立的、最多五秒的清理上下文释放各发送工作线程持有的任务租约。清理成功后重启可立即重新领取任务；硬终止或清理写入失败时，仍由最长一分钟的租约到期恢复。每次释放校验领取 token，旧发送者不能释放新领取者的任务。ntfy 已接收而本地尚未确认时仍可能重复投递。

Graph 邮件请求和 OAuth 刷新都支持 Retry-After 的秒数与 HTTP 日期；已经过去的日期或无效值使用正常退避。有效服务端等待时间长于正常退避时，以服务端时间为准。

守护进程和 CLI 必须使用同一数据目录/Unix socket。只允许一个守护进程持有数据库锁。用指定 user 启动容器时，数据目录和秘密文件必须属于该 UID 或对其可读写。Unix socket 和文件锁依赖 Linux；不支持原生 Windows 守护进程。

## 备份

暂停服务后备份数据库和单独的密钥；SQLite WAL 文件不能在运行时随意忽略。保持一致的离线备份最简单。密钥文件须私密；不要与数据库放进同一个不受保护的备份包。恢复错误密钥时服务失败，不会生成新密钥覆盖旧凭据。
