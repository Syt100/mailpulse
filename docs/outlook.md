# Outlook 个人账户接入

第一版采用 Microsoft Graph，支持 Outlook.com/Hotmail 个人邮箱。Microsoft 365 组织账户留作后续版本。

## 注册应用

1. 在 Microsoft Entra 管理中心的应用注册中创建自己的应用，支持账户类型选择组织目录及个人 Microsoft 账户。
2. 记录 Application client ID。设备代码流程不需要在 MailPulse 中保存 client secret。
3. 在身份验证设置中允许公共客户端流程。
4. 添加 Microsoft Graph 的委托权限 Mail.ReadBasic；离线令牌使用 offline_access。MSAL 的标准身份授权也会请求其必要的 OpenID 身份信息。不要为此服务添加邮件写入或发送权限。

添加账户时填写主登录邮箱地址和 client ID。随后执行 account authorize --id。CLI 会给出微软验证页面和短验证码；在浏览器中登录对应账户并同意授权。CLI 轮询授权状态，其他邮箱继续运行。

首次授权要求微软返回的主登录地址与配置地址一致；已有授权账户要求稳定身份 ID 一致。若使用 Outlook 别名，首次配置请使用微软返回的主登录地址。每个 MailPulse 账户分别保存加密缓存，不共用 Refresh Token。

## 增量与恢复

首次 Delta 全量分页只建立标识基线；完成后每 60 秒查询增量。每页独立持久化，重启继续分页。服务不读取正文、附件；读写权限不属于此应用需求。

授权撤销或过期时重新执行 account authorize；成功后调度器自动恢复。Delta 游标失效时 account resync --ignore-history 明确跳过无法确认的增量，保留待发通知。

官方文档：

- https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-device-code
- https://learn.microsoft.com/en-us/graph/api/message-delta
- https://learn.microsoft.com/en-us/graph/outlook-immutable-id
