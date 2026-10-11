# MailPulse

[中文文档](README.zh-CN.md)

A lightweight, self-hosted notification aggregator for Gmail, QQ Mail and personal Outlook accounts. MailPulse publishes plain-text notifications to ntfy; your existing ntfy-to-wxpusher bridge can deliver them to your phone.

MailPulse is a notification service. It reads metadata, keeps mail at the original provider and does not mark messages as read. Gmail and QQ use TLS IMAP with IDLE and periodic checks. Personal Outlook uses Microsoft Graph Delta with device-code OAuth authorization.

## Build and start

Requires Linux and Go 1.27.2 or newer. Docker deployment is described in [deployment.md](docs/deployment.md).

```sh
go build -o dist/mailpulse ./cmd/mailpulse
./dist/mailpulse --data-dir ./data --key-file ./secrets/master.key init
./dist/mailpulse --data-dir ./data --key-file ./secrets/master.key serve
```

Run management commands in another terminal. Global options precede the command. The daemon must own the same data directory; management commands use a private Unix socket and do not write the database directly.

```sh
./dist/mailpulse --data-dir ./data status
./dist/mailpulse --data-dir ./data notify configure --url https://ntfy.example.com --topic emails --token-file ./secrets/ntfy-token
./dist/mailpulse --data-dir ./data notify test
./dist/mailpulse --data-dir ./data account add --provider gmail --address demo@example.com --alias Gmail --secret-file ./secrets/gmail-password
./dist/mailpulse --data-dir ./data account add --provider qq --address demo@qq.com --alias QQ --secret-file ./secrets/qq-code
./dist/mailpulse --data-dir ./data account add --provider outlook --address demo@outlook.com --alias Outlook --client-id 00000000-0000-0000-0000-000000000000
./dist/mailpulse --data-dir ./data account authorize --id ACCOUNT_ID
```

Examples use fictitious addresses, domains and IDs; replace them locally. Secrets must be private files (0600/0400), hidden terminal input or stdin. Password and token command-line flags are intentionally unavailable. Create ntfy's topic permissions/token separately, then subscribe with your existing bridge.

## Behavior and controls

- Initial synchronization establishes a baseline and does not notify existing mail. Mail arriving during the first baseline may also be skipped; wait for the account to become `running` before testing new mail.
- Reconnects and ordinary restarts resume durable state. A queue persists failures while ntfy is unavailable. At-least-once delivery means rare duplicates are possible after an ambiguous HTTP success or crash.
- Only INBOX is watched in v0.1. A previously unseen message moved into INBOX can be treated as new; read/unread updates and removed Graph entries are ignored.
- Invalid UIDVALIDITY or expired Graph state stops the account with `needs_resync`. `account resync --id ACCOUNT_ID --ignore-history` explicitly discards the uncertain interval and establishes a new baseline; queued jobs remain.
- First matching rule wins, conditions within a rule use AND, and no match means notify with priority 3. Use account IDs for account filters.
- `account pause/resume` changes scanning; queued notifications can still send while an account is paused. `account remove --id ACCOUNT_ID --yes` deletes the account, credentials, state and queued jobs.
- `outbox list` shows delivery IDs/statuses without mail payloads (up to 1000 rows). `outbox retry --id JOB_ID` requeues an unsent failure. Updating ntfy configuration unblocks authentication failures.

```sh
./dist/mailpulse --data-dir ./data rule add --order 10 --action ignore --domain ads.example.com
./dist/mailpulse --data-dir ./data rule add --order 20 --action notify --account ACCOUNT_ID --priority 5 --subject-contains security --hide-subject
./dist/mailpulse --data-dir ./data rule list
```

Credentials and OAuth caches are encrypted at rest with a separate AES-GCM key. Losing the key loses access to saved credentials. Keep the key in a separate protected backup, and never commit either the key or the database. Subjects and senders exist in pending notification payloads; successfully sent payloads are removed after seven days. Do not treat a database backup as free of personal data.

`/healthz` checks the process/database; `/readyz` checks scheduler/sender readiness. An individual account's authentication failure does not fail the whole container. No public management API or Web UI is included; application services are reusable for that later work.

## Provider setup and validation

- [Gmail](docs/gmail.md)
- [QQ Mail](docs/qq.md)
- [Personal Outlook](docs/outlook.md)
- [Operations and troubleshooting](docs/operations.md)
- [Validation and limitations](docs/validation.md)

Automated checks use local TLS/HTTP servers and fake credentials. Real Gmail, QQ, Outlook and handset delivery must be verified in your deployment. Organizational Microsoft 365 accounts are outside the v0.1 acceptance scope.
