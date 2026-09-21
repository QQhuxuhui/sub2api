# Payment settlement protocol 240

This upgrade serializes crypto ownership decisions in PostgreSQL, stores pending-review confirmations, and replaces audit-text scans and unbounded claim-ID lists with indexed queries. Migration 239 remains unchanged.

## Deployment

1. Back up the database, particularly `payment_orders`, `payment_audit_logs`, and `payment_transaction_claims`.
2. Stop and drain all old application instances, including payment reconciliation workers. Deploy both sites separately if they use different databases.
3. Start the updated binary and let migration 240 finish before accepting payment traffic. The migration locks the audit and claim tables while backfilling historical ownership.
4. Replace every old replica before resuming normal traffic. Database triggers reject pre-240 claim writes and Epusdt USDT transitions to PAID. This also covers old callbacks that reuse an existing claim and skip INSERT. Explicitly identified non-Epusdt providers (including EasyPay custom `usdt`) retain their existing path; legacy USDT orders without provider identity are conservatively fenced.

The protocol marker is transaction-local and set only by the new settlement transaction helper. Do not disable the triggers or set the marker manually to make an old binary work. Rolling back to a pre-240 binary requires a coordinated database/application recovery; switching just the image will leave old payment writers fenced.

## Migration failures

Malformed ownership JSON, duplicate hashes owned by different orders, missing manual transfer times, and orphaned claims with unknown provenance abort the entire migration. The error identifies the audit/claim row. Original evidence is retained; no arbitrary owner is chosen.

Restore the implicated order/audit evidence from the gateway or backup, verify the actual owner, then retry the migration. Do not delete the conflicting claim just to make startup succeed. Hex hashes are canonicalized; opaque `epusdt:` references retain case. Early hashless `ORDER_PAID` records with an Epusdt trade number are backfilled too.

## Pending review

A normal hashless confirmation is still credited when it does not overlap a manually settled transfer. Overlap means a potential transfer falls within an order's lifetime: from creation minus two minutes through the later of its expiration and creation plus 24 hours. Callback arrival time is not used as a transfer timestamp. This rule is intentionally conservative across all gateway instances because wallet ownership across instances is not established by the current provider API. Two legitimate overlapping payments can require manual verification.

An ambiguous confirmation commits its gateway claim, evidence, `review_pending=true`, and one `PAYMENT_REVIEW_REQUIRED` audit. It does not credit the balance. The webhook acknowledges the durable record; database/commit failures still request retries. The existing admin order detail displays the review audit and conflicting order ID.

Obtain the actual transaction hash from the gateway or payer. A signed gateway callback with an unused hash resolves review and performs fulfillment, including after the normal expiry grace period. For supported verified chains, the existing admin “settle by transaction hash” action can also resolve the order. A hash already owned by another order stays blocked and the review evidence is preserved. Do not blindly recharge an ambiguous order by hand.

Claims and their time/evidence fields survive refunds and order deletion. Different payment types continue to use their existing fulfillment paths. TG contact icons are unrelated to this upgrade.

## Verification

Run Go tests with `-tags=unit`. Set `SUB2API_TEST_PAYMENT_POSTGRES_DSN` to a disposable PostgreSQL database to run actual migration and independent-connection races. Tests create and remove their own schemas. In particular run `TestPaymentSettlementInterleavingsPostgres`, `TestPaymentSettlementMigrationPostgres`, `TestPaymentSettlementLargeHistoryPostgres`, and `TestPaymentTransactionClaimsPostgres`.
