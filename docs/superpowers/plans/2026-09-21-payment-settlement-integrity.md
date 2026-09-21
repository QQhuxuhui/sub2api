# Payment settlement integrity

Approved scope: fix settlement consistency and unbounded lookups on dev and gptniubi. Keep each branch's contact UI intact.

## Design

- Serialize payment ownership changes using a PostgreSQL transaction advisory lock. External verification runs before taking the lock. Recheck ownership after locking.
- Persist manual transfer evidence and hashless gateway confirmations in the durable claim table. Ambiguous confirmations retain their evidence and a review flag without crediting the order. A later known hash can resolve them. Claim evidence survives order deletion.
- Missing transfer identity is uncertainty, not evidence of a distinct payment. Compare against order lifetime rather than callback arrival time. Preserve idempotent same-order retries.
- Add a forward migration, leaving 239 unchanged. Backfill legacy audits with canonical hashes, reject malformed, incomplete or conflicting ownership evidence visibly. Database triggers reject old payment writers that omit the transaction-local protocol marker; drain old processes for the deployment.
- Use indexed claim queries and correlated EXISTS instead of audit substring scans or materialized ID lists.

## Execution

- [x] Add and run failing sequential reverse-order/delayed-confirmation tests.
- [x] Extend claim schema, generate Ent, implement migration and consistent claim writes.
- [x] Implement protected double-sided checks and persisted review handling.
- [x] Test real PostgreSQL independent connections, rollback, migration, dirty legacy data and normal independent payments.
- [x] Review, commit dev, apply focused commits to a gptniubi worktree, and test both branches.

## Results

Both branches pass the targeted payment/service/handler/admin unit tests with actual PostgreSQL migration and concurrency checks enabled, payment provider/chain tests, `go build ./...`, and frontend type checking. The concurrency/review suite also passed Go's race detector on dev. A 70,000-claim regression checks the previous unbounded-ID-list failure. Independent final code review found no blocking issues. Deployment has not been performed.

## Acceptance

Run settlement tests with `go test -tags=unit`, not plain `go test`. Set `SUB2API_TEST_PAYMENT_POSTGRES_DSN` for PostgreSQL checks and confirm they execute rather than skip. No network payments or production database operations are needed. Keep claims through refunds/deletions and maintain an audit trail for review decisions.
