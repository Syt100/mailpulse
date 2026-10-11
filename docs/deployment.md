# Docker 部署

镜像工作流在合并到 main 或推送 v 开头的版本标签后，将通过测试的 amd64/arm64 镜像发布到 `ghcr.io/syt100/mailpulse`。开发 PR 只构建和扫描，当前还没有已发布版本。首次部署可从开发分支本地构建：

```sh
docker build -t ghcr.io/syt100/mailpulse:latest .
sudo install -d -m 700 -o 10001 -g 10001 data secrets
sudo sh -c 'umask 077; openssl rand 32 > secrets/master.key'
sudo chown 10001:10001 secrets/master.key
sudo chmod 600 secrets/master.key
docker compose -f deploy/compose.yaml up -d
docker compose -f deploy/compose.yaml exec mailpulse mailpulse status
```

只在全新安装时生成密钥。已有数据库必须使用原来的 32 字节密钥；覆盖密钥会使保存的凭据无法解密。启动时自动建立新数据库。密钥和数据库分别备份；保留 `mailpulse.db`、WAL 等数据库文件时，应先停止服务，再完整备份 data 目录。密钥独立保存于受保护的位置。

默认进程 UID/GID 为 10001。Compose 启用 init、只读根文件系统、30 秒停止宽限和自动重启，不映射主机端口。`/data` 必须可写，`/run/secrets/mailpulse_key` 只读。Docker Compose 的文件型 secret 使用宿主文件权限；宿主密钥必须由进程 UID 读取，不能依赖 Compose 的 mode/uid 字段改变它。

自定义 UID 示例：先把 data 和密钥属主改成 `12345:12345`，然后设置 `MAILPULSE_UID=12345 MAILPULSE_GID=12345` 执行每次 Compose 命令。密钥仍为 0600，data 目录仍为 0700。

## 配置通知和账户

以下地址和 topic 都是占位值；替换为自己的配置。ntfy Token 通过终端隐藏输入或私有文件的标准输入传递，不放入命令参数：

```sh
docker compose -f deploy/compose.yaml exec mailpulse mailpulse notify configure --url https://ntfy.example.test --topic your-topic
docker compose -f deploy/compose.yaml exec mailpulse mailpulse notify test
docker compose -f deploy/compose.yaml exec mailpulse mailpulse account add --provider gmail --address example@gmail.com --alias Gmail
docker compose -f deploy/compose.yaml exec mailpulse mailpulse account add --provider qq --address 123456789@qq.com --alias QQ
docker compose -f deploy/compose.yaml exec mailpulse mailpulse account add --provider outlook --address example@outlook.com --alias Outlook --client-id YOUR_PUBLIC_CLIENT_ID
docker compose -f deploy/compose.yaml exec mailpulse mailpulse account list
docker compose -f deploy/compose.yaml exec mailpulse mailpulse account authorize --id OUTLOOK_ACCOUNT_ID
```

Gmail 使用应用密码，QQ 使用 IMAP 授权码；Outlook 需要个人账户支持的公共客户端应用。设备授权打印短期代码和微软登录网址；只在该官方页面登录，不把代码或凭据发到聊天中。完整步骤见 [Gmail](gmail.md)、[QQ](qq.md) 和 [Outlook](outlook.md)。

ntfy 位于另一容器时，需要加入同一个 Docker 网络，并使用可从 MailPulse 容器访问的 URL。容器内 `127.0.0.1` 指向 MailPulse 自身。此程序提交到 ntfy 后即认为发送成功，手机和 wxpusher 的后续转发需要单独验收。

## 更新和检查

```sh
docker compose -f deploy/compose.yaml pull
docker compose -f deploy/compose.yaml up -d
docker compose -f deploy/compose.yaml ps
docker compose -f deploy/compose.yaml exec mailpulse mailpulse status
docker compose -f deploy/compose.yaml exec mailpulse mailpulse outbox list
```

使用固定版本或 `sha-完整commit` 镜像标签可以保持部署版本可追踪。更新前备份数据库和密钥。需要恢复时，先停止服务，再恢复同一备份对应的数据库和密钥。队列为至少一次投递；ntfy 已接收但进程尚未保存结果时重启，可能重复一次。

本地容器验证：`MAILPULSE_TEST_IMAGE=ghcr.io/syt100/mailpulse:latest go test -v ./integration`。CI 分别构建、扫描和验证两种架构，包括非 root、自定义 UID、只读根目录、停止退出码和队列重启持久化。当前开发环境没有 Docker，镜像大小、内存和真实邮箱验收结果见 [验证记录](validation.md)。
