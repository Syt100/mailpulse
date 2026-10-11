# Gmail 接入

启用 Google 两步验证，并在账户允许的情况下创建应用专用密码。使用 Gmail 主邮箱地址和这个应用密码连接 imap.gmail.com:993。普通 Google 密码不可代替应用密码；若账户策略不允许应用密码，该账户不在首版认证方式的覆盖范围内。

把应用密码存入权限 0600 的独立文件，再运行 README 中 account add 命令。CLI 会移除 Gmail 应用密码显示格式中的空格。账户初始化完成并显示 running 后，从另一个账户向收件箱发一封新邮件测试。

第一版只监听 INBOX。被 Gmail 规则直接归档、跳过收件箱的邮件不会提醒。IDLE 保持连接，默认每 60 秒再检查 UID，补查重连期间仍可读取到的邮件。

官方说明：https://support.google.com/accounts/answer/185833
