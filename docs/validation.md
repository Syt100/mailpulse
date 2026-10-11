# MailPulse 验证记录

自动化测试覆盖原子回滚、队列容量、事件重放、基线、规则、凭据加密、发送响应分类、过期租约、只读 IMAP、Graph 分页和链接限制、多账户隔离及 CLI 请求。测试使用虚构账户和本地模拟服务器。

当前开发环境禁止 Unix socket 创建，权限测试遇到内核 EPERM 时明确跳过；Linux CI 必须另外运行守护进程和 CLI 烟测。开发环境没有 Docker，容器健康、指定 UID、只读根文件系统和实际镜像大小需由 CI 验证。

本地已运行 `go vet ./...`、`go test ./...`、`go test -race ./...`，以及 Linux amd64/arm64 的 CGO 关闭编译。`scripts/smoke.sh` 已在本地尝试，在私有 Unix socket 创建阶段受到上述 EPERM 限制，未完成完整烟测。容器测试在未设置 `MAILPULSE_TEST_IMAGE` 时明确跳过，在 CI 中设置该变量后必须执行。

CI 的 Go 检查包含原生守护进程、私有控制、ntfy 发送和重启烟测。容器工作流分别检查 amd64/arm64 镜像漏洞、UID 10001、自定义 UID、只读根目录、健康检查、SIGTERM 和待发队列重启恢复；两种架构均通过后，main/版本标签才能发布镜像。完整结果以开发 PR 上最新提交的检查为准。

## 已观察到的 CI 结果

2026-10-11，远端修复提交 `9fb6833a42db191922f3d80a517872d034486501` 的 [Go 检查](https://github.com/Syt100/mailpulse/actions/runs/38120402016) 和 [容器检查](https://github.com/Syt100/mailpulse/actions/runs/38120402012) 均通过，发布步骤因 PR 触发而跳过。此前 CI 使用 Go 1.27.0 检出标准库漏洞；最低版本升至 1.27.2 后完整检查通过。

下表是同一次容器检查的实际观察：0 个活跃邮箱、1 个虚构待发任务、默认四个发送工作线程。镜像字节数来自 Docker image inspect 的 .Size；内存来自 docker stats 的容器使用量。arm64 在 QEMU 下运行，数字仅作这个空闲测试的记录，不能外推到真实多账户负载。

| 架构 | 镜像字节数 | 默认 UID 内存 | 自定义 UID 内存 |
|---|---:|---:|---:|
| amd64 | 22,615,122 | 7.855 MiB | 8.059 MiB |
| arm64 | 22,959,407 | 6.121 MiB | 7.953 MiB |

后续日志和附加回归测试提交会重新运行 CI；最新提交的结果应在 [PR #2](https://github.com/Syt100/mailpulse/pull/2) 检查。独立审查发现的三项并发/恢复问题、修复证据和实施裁定见 [实现记录](implementation-notes.md)。

## 用户真机验收

以下项目尚未使用真实账号完成，不能视作已验证：

1. Gmail 应用密码、QQ 授权码、Outlook 个人 OAuth 各接入一个账户。
2. 等待各账户 running，确认首次不推历史。
3. 每个账户收到新邮件，确认 ntfy、wxpusher 和手机收到通知。
4. 正常重启后不重复正常已处理邮件；断线恢复后补查仍在邮箱中的邮件。
5. 停止 ntfy 后发送新邮件，再恢复 ntfy，确认队列补发。
6. 让一个账户认证失败，确认其他账户继续通知。
7. 测试过滤、隐藏主题、高优先级和 account pause/resume。

执行结果应补充测试日期、版本/commit、服务商和现象。不要记录邮箱密码、Token、完整通知正文或实际个人邮箱地址。
