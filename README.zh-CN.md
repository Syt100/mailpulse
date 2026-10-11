# MailPulse

轻量级、自部署的多邮箱通知聚合服务。监听 Gmail、QQ 邮箱和 Outlook 个人邮箱，将新邮件元数据推送到 ntfy，再由现有转发器送达 wxpusher。

## 第一版能力

- 一个进程管理多个账户；Gmail/QQ 使用 TLS IMAP IDLE，Outlook 使用 Graph 增量查询。
- SQLite 持久化同步游标、去重记录和待发通知；暂时无法推送时保留队列。
- 邮件只读，只获取主题、发件人和接收时间，不获取正文、附件，不修改已读状态。
- CLI 管理账户、微软授权、规则、状态和失败通知；为未来 Web UI 保留应用服务。
- 账户秘密和 OAuth 缓存加密保存；非 root Docker 部署，支持 amd64/arm64。

## 本地运行

需要 Linux 和 Go 1.27.2 或更新版本。Docker/Portainer 部署见 [部署说明](docs/deployment.md)。

```sh
go build -o dist/mailpulse ./cmd/mailpulse
./dist/mailpulse --data-dir ./data --key-file ./secrets/master.key init
./dist/mailpulse --data-dir ./data --key-file ./secrets/master.key serve
```

另开终端管理同一个服务。全局参数必须写在命令之前：

```sh
./dist/mailpulse --data-dir ./data notify configure --url https://ntfy.example.com --topic emails --token-file ./secrets/ntfy-token
./dist/mailpulse --data-dir ./data notify test
./dist/mailpulse --data-dir ./data account add --provider gmail --address demo@example.com --alias Gmail --secret-file ./secrets/gmail-password
./dist/mailpulse --data-dir ./data account add --provider qq --address demo@qq.com --alias QQ --secret-file ./secrets/qq-code
./dist/mailpulse --data-dir ./data account add --provider outlook --address demo@outlook.com --alias Outlook --client-id 00000000-0000-0000-0000-000000000000
./dist/mailpulse --data-dir ./data account authorize --id ACCOUNT_ID
./dist/mailpulse --data-dir ./data status
```

示例地址、域名和 ID 均为虚构，需在自己的机器上替换。秘密文件权限应为 0600 或 0400。也支持隐藏终端输入及 stdin，不支持把密码或 Token 直接写入命令参数。Outlook 首次授权应使用配置的主登录邮箱地址；不能把另一个微软账户的授权写入已有账户。

## 通知规则与恢复

首次同步不推送历史，首次基线建立期间收到的邮件也可能不提醒；等状态变成 `running` 后再发测试邮件。正常重启或断线会继续使用已有游标。队列采用至少一次投递，极端情况下可能重复通知。ntfy 接收成功不代表手机已经收到。

默认只监听收件箱；之前没见过、手动移入收件箱的邮件也可能触发通知。Graph 的已读状态变化和删除事件不触发新邮件通知。规则按顺序首个匹配生效，一个规则中的条件为 AND，无匹配时优先级为 3。

```sh
./dist/mailpulse --data-dir ./data rule add --order 10 --action ignore --domain ads.example.com
./dist/mailpulse --data-dir ./data rule add --order 20 --action notify --priority 5 --subject-contains security --hide-subject
./dist/mailpulse --data-dir ./data account pause --id ACCOUNT_ID
./dist/mailpulse --data-dir ./data account resume --id ACCOUNT_ID
./dist/mailpulse --data-dir ./data outbox list
./dist/mailpulse --data-dir ./data outbox retry --id JOB_ID
```

暂停账户只停止收信，已入队通知仍可发送。`account remove --id ACCOUNT_ID --yes` 会删除账户及其凭据、状态和待发通知。

游标失效时状态为 `needs_resync`，服务不会静默跳过。使用 `account resync --id ACCOUNT_ID --ignore-history` 明确建立新基线；失效期间无法确认的增量将跳过，已有待发通知保留。

请把主密钥和数据库分开备份；不要提交到 Git。数据库中的待发通知包含主题、发件人等个人数据，成功投递七天后会清除其通知内容。密钥遗失后无法解密已有凭据。

## 接入与排查

[Gmail](docs/gmail.md) · [QQ](docs/qq.md) · [Outlook](docs/outlook.md) · [运行与排查](docs/operations.md) · [验证记录](docs/validation.md)

自动化测试使用模拟服务器和虚构凭据。真实邮箱及手机推送需要在你的环境验证。第一版尚无 Web UI，Microsoft 365 组织邮箱不属于本次验收范围。
