# MailPulse v0.1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a deployable multi-account Gmail, QQ and personal Outlook notification service publishing to ntfy, with recoverable synchronization and delivery state.

**Architecture:** A Go modular monolith has shared application services behind CLI and a private Unix socket. IMAP and Graph produce bounded batches; SQLite commits event decisions, delivery jobs and cursors atomically; a separate sender drains the outbox.

**Tech Stack:** Go 1.27, standard-library HTTP and slog, modernc.org/sqlite, emersion/go-imap/v2, MSAL Go. Module path: `github.com/Syt100/mailpulse`. Pin compatible dependency releases and go.sum during Task 1; upgrade version choices only when compatibility or security requires it.

**Spec:** `docs/superpowers/specs/2026-10-11-mailpulse-design.md`

## Global Constraints

- No Web UI, public management REST API, message bodies, attachments or writes to mailboxes in v0.1.
- Gmail and QQ use IMAP over TLS on port 993, INBOX by default; use read-only selection and metadata fetching.
- Outlook uses Graph v1.0, consumers authority, delegated Mail.ReadBasic and offline_access, device code authorization.
- Default scan interval: 60 seconds; ntfy priority: 3, valid range: 1–5.
- Outbox capacity: 10000 pending jobs; sender concurrency: 4; retry delay: 5 seconds to 5 minutes with jitter, respecting Retry-After.
- Successful notification payloads are removed after 7 days; unsent jobs never expire automatically; identifiers persist until account removal or explicit reset.
- AES-GCM secret encryption with a separate key file; absent/wrong keys fail closed; no passwords in command arguments or default logs.
- First baseline sends no history; normal reconnect/restart preserves cursors. Invalid UIDVALIDITY or delta state enters needs_resync and requires explicit resync.
- At-least-once delivery; success from ntfy does not imply handset delivery.
- Runtime owns database writes; management uses a private Unix socket. Non-root UID 10001; amd64 and arm64 images.
- Automated tests use fake credentials and local servers. Real mailbox validation must be reported separately.

## Review Focus

1. Malicious Graph nextLink or HTTP redirects must never receive the mailbox bearer token (Task 5).
2. Two authorized Outlook accounts must not share an MSAL cache or select one another's identity (Task 5).
3. A stalled OAuth login must not block account scans or require an HTTP request to remain open for 15 minutes (Tasks 5–7).
4. Malformed MIME subjects, Chinese names and duplicate/out-of-order provider records must remain safe and deterministic (Tasks 1, 4, 5).
5. Disk-full transactions, wrong encryption keys and stale sender leases must preserve recoverability and expose errors (Tasks 1–3, 6).

## Shared Data and Interfaces

Define these concrete models in `internal/domain/models.go`; identifiers are strings and times are UTC:
- Account: ID, Alias, Provider, Address, Enabled, Settings (provider-specific non-secret JSON).
- MailEvent: Key, AccountID, Folder, SenderAddress, SenderName, Subject, ReceivedAt, ClickURL, Kind (added/state_changed/removed).
- Cursor: AccountID, Folder, Generation, Position, Initialized.
- Batch: Events []MailEvent, Next Cursor, Baseline bool.
- Decision: EventKey, Notify bool, Payload Notification.
- Notification: Title, Message, Priority, Click.
- Delivery: ID, AccountID, EventKey, Payload, Attempt, NextAttemptAt, LeaseUntil.
- AccountStatus: State, LastSuccessAt, ErrorCode; states include initializing/running/backoff/auth_required/needs_resync/paused and degraded.
- Rule: ID, Order, AccountID, SenderAddress, SenderDomain, SubjectContains, Action, Priority, HideSubject, HideSender.

Application interfaces in `internal/domain/ports.go`:
- BatchStore.CommitBatch(ctx context.Context, batch Batch, decisions []Decision) error.
- Provider.Scan(ctx context.Context, account Account, cursor Cursor) (Batch, error).
- Provider.Wait(ctx context.Context, account Account, interval time.Duration) error; IMAP IDLE or Graph timer, cancellable.
- Publisher.Publish(ctx context.Context, notification Notification) error.
- SecretStore.Load(ctx context.Context, accountID string) ([]byte, error); Save(ctx context.Context, accountID string, plaintext []byte) error.

Providers return typed errors: authentication_required, needs_resync, transient, throttled. No provider advances persisted cursors itself. SQL implementation may expose additional focused repository methods; application services depend on interfaces.

## Task 1: Atomic event processing, rules and durable queue

**Files:** `go.mod`, `go.sum`, `.gitignore`, `internal/domain/{models,ports,errors,rules}.go`, `internal/app/processor.go`, `internal/store/sqlite/{store,migrations,batches,outbox}.go`; matching `*_test.go`.

**Interfaces:** Produces models above; `sqlite.Open(ctx context.Context, path string, capacity int) (*Store, error)`, `app.NewProcessor(store domain.BatchStore) *Processor`, `(*Processor).Process(ctx context.Context, batch domain.Batch, rules []domain.Rule) error`. Store implements CommitBatch and `ClaimDeliveries(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.Delivery, error)`.

- [x] Write tests TestCommitBatchAtomic, TestCapacityDoesNotAdvanceCursor, TestReplayDoesNotEnqueueTwice, TestBaselineSavesKeysWithoutNotification, TestRulesFirstMatchAND, TestChineseNotification. Assertions: a failed transaction leaves both cursor and job count unchanged; capacity 10000 rejects the next notifying event; duplicate key has one job; baseline has zero jobs; rule order and AND conditions are enforced; default priority equals 3.
- [x] Run `go test ./internal/domain ./internal/app ./internal/store/sqlite`; confirm the new behavior is missing before implementation.
- [x] Implement migrations, unique keys, bounded transactions and rules. Use one SQLite connection for writes, busy timeout, WAL and explicit transactions. Initialize/close DB safely; set DB directory permissions. Store no message bodies. Processor sanitizes headers, handles empty subjects, generates bounded plain-text notification payloads and ignores non-added events.
- [x] Run the same tests; inject a write failure before commit and reopen the file DB to verify rollback and replay. `go test -race ./internal/domain ./internal/app ./internal/store/sqlite` must pass.
- [x] Commit `feat: add atomic mail processing and persistent outbox`.

## Task 2: Account management and encrypted secrets

**Files:** `internal/security/{keys,cipher}.go`, `internal/store/sqlite/{accounts,secrets,rules}.go`, `internal/app/{accounts,rules}.go`, `internal/config/config.go`, matching tests.

**Interfaces:** `security.LoadKey(path string) ([]byte, error)`, `security.Encrypt(key, plaintext, associatedData []byte) ([]byte, error)`, `security.Decrypt(key, ciphertext, associatedData []byte) ([]byte, error)`. Store implements SecretStore. `app.NewAccountService(store AccountRepository, secrets domain.SecretStore) *AccountService` exposes Add/List/Pause/Resume/Remove/Resync using typed input/result structs and context.

- [x] Write TestWrongKeyFailsClosed, TestAccountSecretsBoundToAccountID, TestSecretsAbsentFromAccountJSON, TestPausePreservesState, TestRemoveRequiresExplicitConfirmation, TestResyncRequiresIgnoreHistory, TestResyncPreservesOutbox. Assert decrypt fails with a wrong key or account ID; account list has no secret fields; pause leaves cursors/outbox unchanged; resync clears baseline/processed identifiers but retains existing queued jobs.
- [x] Run `go test ./internal/security ./internal/app ./internal/store/sqlite` and record expected failures.
- [x] Implement 32-byte key loading and random nonce AES-GCM with account ID as associated data. Init creates a key only for a new store using exclusive creation and mode 0600; existing stores verify an encrypted key-check record before workers start. Encrypt IMAP credentials, ntfy token and per-account MSAL caches. Validate provider settings and rule priorities. Pause stops scanning while queued jobs may continue sending; removal deletes account data including queued jobs only after explicit confirmation.
- [x] Verify tests and inspect serialized DTOs and error logs for leaks; migrations must be idempotent.
- [x] Commit `feat: manage accounts and encrypt credentials`.

## Task 3: ntfy sender, retries and recovery

**Files:** `internal/notify/ntfy/{client,errors}.go`, `internal/app/delivery.go`, `internal/store/sqlite/delivery.go`, matching tests.

**Interfaces:** `ntfy.NewClient(baseURL, topic string, tokenSource func(context.Context) (string, error), client *http.Client) (*Client, error)`; Client implements Publisher. `app.NewDeliveryService(store DeliveryRepository, publisher domain.Publisher) *DeliveryService`, `(*DeliveryService).Run(ctx context.Context, concurrency int) error`.

- [x] Write TestUTF8JSONPublish, TestRetryAfter429, TestTransientRetries, TestAuthFailureBlocksUntilConfigurationChanges, TestPermanentFailureIsInspectable, TestRestartRecoversLease, TestSentPayloadPrunedAfterSevenDays. Assert token stays in the Authorization header; 401/403 do not hot-loop; 429 cannot retry before Retry-After; a failed publish never marks sent; expired lease becomes eligible; pending jobs are never pruned.
- [x] Run `go test ./internal/notify/ntfy ./internal/app ./internal/store/sqlite` and confirm failures.
- [x] Implement bounded JSON payload publication, request timeouts and no cross-origin authenticated redirects. Parse Retry-After seconds and HTTP dates; exponential backoff with jitter and injected clock/randomness for tests. Claim jobs with persisted leases, mark sent only after accepted response, expose failed jobs and explicit retry. Keep response bodies out of errors/logs; run cleanup on bounded intervals.
- [x] Verify recovery with a file DB and simulated HTTP timeout after server acceptance; document potential duplicate delivery. Run race tests.
- [x] Commit `feat: publish durable ntfy notifications with retry`.

## Task 4: Gmail and QQ IMAP adapter

**Files:** `internal/providers/imap/{client,scan,idle,presets}.go`, `internal/providers/imap/{scan,idle}_test.go`, `internal/providers/imap/testserver_test.go`.

**Interfaces:** `imap.NewProvider(secrets domain.SecretStore, dialer Dialer) *Provider` implements domain.Provider. Dialer isolates go-imap library types from application services.

- [x] Write TestReadOnlyBaseline, TestUIDScanIncludesReadMail, TestUIDValidityChanged, TestIdleDisconnectBackfills, TestNoIdleFallsBackToPoll, TestUnorderedUIDs, TestMalformedMIMESubject. Assert initial baseline emits no notification; scans use UID not UNSEEN; no STORE/body fetch is issued; FETCH reads only metadata; UID validity change returns needs_resync; interrupted IDLE reconnects and finds missed UIDs without duplicates.
- [x] Run `go test ./internal/providers/imap` against a local TLS IMAP server and verify failures before coding.
- [x] Implement TLS certificate verification, read-only INBOX selection, UIDNEXT-based first baseline, bounded UID batches and header decoding. Filter returned UIDs greater than persisted high-water explicitly (IMAP reversed UID ranges can include old messages). Preserve original cursor until CommitBatch succeeds. End IDLE periodically, serialize commands per connection, cancel waits immediately on shutdown and retry with jitter.
- [x] Verify tests, including empty inbox, deleted last UID, login failure and cancel during IDLE. Run race tests.
- [x] Commit `feat: watch Gmail and QQ using read-only IMAP`.

## Task 5: Outlook device authorization and Graph Delta

**Files:** `internal/providers/graph/{oauth,cache,client,delta,links}.go`, `internal/app/authorization.go`, matching tests.

**Interfaces:** `graph.NewProvider(tokens TokenSource, client *http.Client) *Provider` implements domain.Provider. `TokenSource.AccessToken(ctx context.Context, accountID string) (string, error)`. `(*AuthorizationService).Start(ctx context.Context, accountID string) (AuthorizationStart, error)`, `(*AuthorizationService).Status(ctx context.Context, sessionID string) (AuthorizationStatus, error)`; Start returns only initiating CLI's verification URL/code/expiry/session ID, Status returns progress without tokens.

- [x] Write TestDeviceFlowIsExplicitAndCancellable, TestTwoAccountCacheIsolation, TestRefreshPersistsBeforeUse, TestGraphInitialPaginationSilent, TestGraphPageReplay, TestDeltaExpiredNeedsResync, TestMaliciousNextLink, TestGraphRedirectDoesNotLeakToken, TestIgnoreRemovedAndStateChanges. Assert 60-second default polling; every page has ImmutableId preference; initial pages save identifiers and zero jobs; crashed second page resumes without duplicates; cross-host links are rejected before request; unauthorized identity cannot overwrite another account cache.
- [x] Run `go test ./internal/providers/graph ./internal/app` with fake token and Graph servers, confirming failures.
- [x] Implement MSAL public-client device flow and encrypted per-account cache, consumers authority and scopes from constraints. Bind the returned identity to the account's stable MSAL account identity; do not parse Microsoft access-token claims. Serialize refresh, persist cache changes before accepting tokens, redact all error details. Authorization runs as a cancellable background session with expiry, so other workers continue.
- [x] Implement changeType=created Delta with selected basic fields; follow opaque validated links and preserve case-sensitive IDs. Initial full synchronization sets baseline only at final deltaLink. Ignore removed/state-only entries even if returned by a filtered request; use processed identifier to suppress previously seen items. Return needs_resync on invalid state and respect throttling.
- [x] Verify tests, including cache write failure, mixed removed entries, empty pages with nextLink and repeated pages. Run race tests.
- [x] Commit `feat: authorize personal Outlook and track Graph changes`.

## Task 6: Supervisor, local control and health

**Files:** `internal/runtime/{supervisor,lock,lifecycle}.go`, `internal/transport/control/{server,client,protocol}.go`, `internal/transport/http/health.go`, matching tests.

**Interfaces:** `runtime.NewSupervisor(accounts AccountRepository, providers map[string]domain.Provider, processor *app.Processor, deliveries *app.DeliveryService) *Supervisor`, `(*Supervisor).Run(ctx context.Context) error`. `control.NewServer(path string, services Services) *Server`, `health.Handler(probe Probe) http.Handler`.

- [x] Write TestOneAccountFailureIsIsolated, TestSecondDaemonRejected, TestControlSocketPermissions, TestDiskFailureDoesNotAdvanceCursor, TestShutdownRecoversInFlightJob, TestHealthAllowsAccountAuthFailure, TestReadyReportsStoppedSender, TestAuthorizationDoesNotBlockScans.
- [x] Run `go test ./internal/runtime ./internal/transport/control ./internal/transport/http` to verify failures.
- [x] Implement an exclusive OS-backed database lock; safe stale socket handling only after acquiring lock; socket permission 0600 and bounded JSON requests. Supervisor owns per-account serial workers, immediate configuration changes and status. On needs_resync/auth_required pause automatic scan until corrected; queue full marks degraded without advancing cursor. Start four sender workers; handle SIGTERM with cancellation and a bounded grace period.
- [x] Implement /healthz for main loop plus DB accessibility and /readyz for operational scheduler/sender, without exposing account identifiers. Listen on :8080 inside container; manage only over Unix socket. Reject oversized or malformed control messages; session completion is polled rather than held in an HTTP call.
- [x] Verify integration tests with two providers and a slow authorization session; run race tests.
- [x] Commit `feat: supervise accounts and expose private controls`.

## Task 7: CLI and usable notification vertical slice

**Files:** `cmd/mailpulse/main.go`, `internal/transport/cli/{root,accounts,rules,authorization,outbox,status}.go`, `configs/mailpulse.example.json`, `README.md`, `README.zh-CN.md`, `docs/{gmail,qq,outlook,operations}.md`, matching tests.

**Interfaces:** `cli.Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error`. CLI invokes typed app services only through control client, except locked offline init.

- [x] Write TestSecretFileInput, TestPasswordNotAcceptedAsFlag, TestCLIStatusRedacted, TestAuthorizeSessionPolling, TestAccountRemoveConfirmation, TestNotifyTestPublishesWithoutMail, TestOutboxRetry, TestDaemonRequiredForMutation. Assert secrets never appear in stdout/status/errors; wrong usage returns nonzero; no concurrent direct DB writes; JSON status uses stable DTOs suitable for future Web UI.
- [x] Run `go test ./internal/transport/cli ./cmd/mailpulse`, confirming missing behavior.
- [x] Implement serve/init; account add/list/pause/resume/remove/authorize/resync; rule add/list/remove; notify test; outbox list/retry; status; version and healthcheck. Use standard flag parsing, terminal-hidden prompts or secret files, environment/JSON config only for runtime settings. Reject unknown fields and invalid URLs early.
- [x] Verify fake-provider end-to-end flow: baseline, arrival, filter, ntfy failure, restart, recovery, explicit reset. Document TLS endpoints, Outlook app registration, encrypted backups, device authorization, initialization boundary, moved-in mail semantics and at-least-once behavior. Examples contain only fictitious addresses and fake tokens.
- [x] Commit `feat: provide MailPulse management CLI and setup guides`.

## Task 8: Container deployment and GitHub delivery

**Files:** `Dockerfile`, `.dockerignore`, `deploy/compose.yaml`, `.github/workflows/{ci,images}.yml`, `docs/deployment.md`, `docs/validation.md`.

**Interfaces:** Single binary `mailpulse`; container data /data, secret /run/secrets/mailpulse_key; image ghcr.io/syt100/mailpulse.

- [x] Add integration checks for TestContainerHealthAsNonRoot and TestCustomUIDCanWriteMountedData; assert UID 10001 default, custom UID works with correctly owned mount, read-only root works, secrets are absent from image and graceful stop preserves pending jobs.
- [x] Implement multi-stage CGO_ENABLED=0 build with CA certificates, OCI labels, binary healthcheck and pinned build/runtime image digests. Compose uses init, restart policy, no published management ports and writable data mount; include explicit directory ownership/key setup.
- [x] CI checks gofmt, go vet, go test, go test -race, builds and govulncheck. Pin action revisions. Build PR images without pushing; default-branch and version-tag workflows can publish amd64/arm64 to GHCR using GITHUB_TOKEN with packages:write. Scan before publication, scope token permissions, and use repository build contexts without secrets.
- [x] Run `go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./cmd/mailpulse`, `govulncheck ./...` and container smoke tests. Record actual image size and memory observations with workload; do not invent figures if Docker is unavailable.
- [x] Commit `build: add non-root containers and GitHub checks`.

## Task 9: Whole-branch review and deployment validation

**Files:** `docs/validation.md`; targeted code/test fixes if review reveals defects.

- [ ] Review spec coverage, credential leakage, cursor atomicity, URL validation, queue ownership and shutdown; investigate any failures before changing code. Each defect receives a reproducing regression test.
- [ ] Re-run only required affected checks, then complete full release checks once. Confirm GitHub CI on the actual latest commit and inspect logs when jobs fail.
- [ ] Open a feature PR with implemented behavior and exact validation, keeping real-mailbox tests explicitly pending until user deployment. Do not merge or tag a release without the corresponding user instruction.
- [ ] Provide user steps for one Gmail, one QQ and one personal Outlook account; test new delivery, no history, reconnect, restart, ntfy outage and isolated auth failure. Never request mailbox passwords/tokens in chat.
- [ ] After user real-device results, record verified outcomes and fix reported failures before release readiness.

## Plan Self-Review

Scope maps to all spec sections: providers (4–5), module/API boundaries (1–2, 6–7), queue/rules (1–3), encryption (2, 5), CLI/health/runtime (6–7), containers/CI (8), real deployment and review (9). Shared types and method names are fixed above. All five Review Focus risks have owning tests. The only intentionally deferred features are those excluded by the approved v0.1 spec.

## Execution Handoff

Recommended method: native execution in this session, followed by an independent whole-branch review. Tasks share persistence and application interfaces, so sequential implementation keeps integration decisions together. Subagent-driven execution is available if the user prefers independent per-task implementation and reviews. Start product implementation after the user reviews this plan and chooses the execution method.

## References

- Go baseline: https://go.dev/doc/go1.27
- MSAL public client: https://github.com/AzureAD/microsoft-authentication-library-for-go
- Graph Delta and device-code references: see approved spec.
