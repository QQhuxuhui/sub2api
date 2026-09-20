package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// gatewayTradeClaimPrefix namespaces claims keyed by the gateway's own trade
// number instead of a chain hash. Epusdt reports no hash in two cases: its
// public query API never carries one, and a parent order paid through a
// sub-order (the payer switched network inside the cashier) is notified with
// the parent row, whose block_transaction_id is empty. Such payments are still
// confirmed by the gateway, so they are fulfilled and deduplicated per trade;
// a signed callback that later brings the real hash adds that claim as well.
const gatewayTradeClaimPrefix = "epusdt-trade:"

// paymentGatewayTradeClaimKey is the claim key for a hashless confirmation.
func paymentGatewayTradeClaimKey(tradeNo string) string {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" || len(tradeNo)+len(gatewayTradeClaimPrefix) > 512 {
		return ""
	}
	return gatewayTradeClaimPrefix + tradeNo
}

func isGatewayTradeClaimKey(key string) bool {
	return strings.HasPrefix(key, gatewayTradeClaimPrefix)
}

// claimPaymentTransaction must run in the transaction that marks the order
// paid. The unique index arbitrates across processes and payment entry points;
// an upsert must never replace the original owner with the proposed order ID.
// It reports whether this call created the claim (false on an idempotent retry).
func claimPaymentTransaction(ctx context.Context, client *dbent.Client, orderID int64, hash string) (bool, error) {
	existing, err := client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(hash)).Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return false, fmt.Errorf("check payment transaction: %w", err)
	}
	if existing != nil && existing.OrderID == orderID {
		return false, nil
	}
	if usedBy, err := paymentTransactionUsedBy(ctx, client, hash, orderID); err != nil {
		return false, fmt.Errorf("check payment transaction: %w", err)
	} else if usedBy != "" {
		return false, paymentTransactionAlreadyUsed(usedBy)
	}
	if err := client.PaymentTransactionClaim.Create().SetTxHash(hash).SetOrderID(orderID).
		OnConflictColumns(paymenttransactionclaim.FieldTxHash).Ignore().Exec(ctx); err != nil {
		return false, fmt.Errorf("claim payment transaction: %w", err)
	}
	claim, err := client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(hash)).Only(ctx)
	if err != nil {
		return false, fmt.Errorf("read payment transaction claim: %w", err)
	}
	if claim.OrderID != orderID {
		return false, paymentTransactionAlreadyUsed(strconv.FormatInt(claim.OrderID, 10))
	}
	return true, nil
}

// hashlessPaidOrderNear finds an order that was fulfilled on a gateway trade
// claim alone — its chain hash is still unknown — and was paid around the given
// block time. Settling another order by a hash from that window could credit
// the very transfer that order was already paid with: a payer can leave one
// order unpaid, pay a second one through a switched network, then present that
// transfer's hash for the first. Returns 0 when there is none.
func hashlessPaidOrderNear(ctx context.Context, client *dbent.Client, blockTime time.Time, exceptOrderID int64) (int64, error) {
	claims, err := client.PaymentTransactionClaim.Query().
		Where(paymenttransactionclaim.TxHashHasPrefix(gatewayTradeClaimPrefix)).All(ctx)
	if err != nil {
		return 0, err
	}
	candidates := make([]int64, 0, len(claims))
	for _, claim := range claims {
		if claim.OrderID != exceptOrderID {
			candidates = append(candidates, claim.OrderID)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	linked, err := client.PaymentTransactionClaim.Query().Where(
		paymenttransactionclaim.OrderIDIn(candidates...),
		paymenttransactionclaim.Not(paymenttransactionclaim.TxHashHasPrefix(gatewayTradeClaimPrefix)),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	hasHash := make(map[int64]bool, len(linked))
	for _, claim := range linked {
		hasHash[claim.OrderID] = true
	}
	hashless := candidates[:0]
	for _, id := range candidates {
		if !hasHash[id] {
			hashless = append(hashless, id)
		}
	}
	if len(hashless) == 0 {
		return 0, nil
	}
	order, err := client.PaymentOrder.Query().Where(
		paymentorder.IDIn(hashless...),
		paymentorder.PaidAtGTE(blockTime.Add(-hashlessPaidBefore)),
		paymentorder.PaidAtLTE(blockTime.Add(hashlessPaidAfter)),
	).Order(dbent.Asc(paymentorder.FieldID)).First(ctx)
	if dbent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return order.ID, nil
}

// A payment is recorded (paid_at) after its transfer is mined: seconds for a
// callback, but up to the order timeout when only the reconcile job or the
// pre-expiry query notices it. The window errs on the wide side.
const (
	hashlessPaidBefore = 2 * time.Minute
	hashlessPaidAfter  = 2 * time.Hour
)

func paymentTransactionAlreadyUsed(orderID string) error {
	return infraerrors.Conflict("TX_ALREADY_USED", "this transaction already settled order "+orderID)
}

// paymentTransactionUsedBy also honors pre-migration audit records. The order
// exclusion permits idempotent retries without ignoring another legacy owner.
func paymentTransactionUsedBy(ctx context.Context, client *dbent.Client, hash string, exceptOrderID int64) (string, error) {
	claim, err := client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(hash)).Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return "", err
	}
	if claim != nil && claim.OrderID != exceptOrderID {
		return strconv.FormatInt(claim.OrderID, 10), nil
	}
	logs, err := client.PaymentAuditLog.Query().Where(
		paymentauditlog.ActionIn("ORDER_PAID", auditActionManualSettled),
		paymentauditlog.OrderIDNEQ(strconv.FormatInt(exceptOrderID, 10)),
		paymentauditlog.DetailContainsFold(hash),
	).All(ctx)
	if err != nil {
		return "", err
	}
	for _, log := range logs {
		var detail struct {
			TxHash string `json:"txHash"`
		}
		if err := json.Unmarshal([]byte(log.Detail), &detail); err != nil {
			return "", fmt.Errorf("read payment transaction audit %d: %w", log.ID, err)
		}
		legacyHash, _ := chainverify.NormalizeTxHash(detail.TxHash)
		if detail.TxHash == hash || legacyHash == hash {
			return log.OrderID, nil
		}
	}
	return "", nil
}

func (s *PaymentService) orderUsingTxHash(ctx context.Context, normalizedHash string) (string, error) {
	return paymentTransactionUsedBy(ctx, s.entClient, normalizedHash, 0)
}

// EVM/TRON hashes share the manual verifier's canonical format. Other signed
// gateway transaction references (e.g. Solana/TON) remain case-sensitive and
// are namespaced so they cannot collide with a canonical hexadecimal hash.
func paymentTxHashFromMetadata(metadata map[string]string) string {
	raw := strings.TrimSpace(metadata["block_transaction_id"])
	if raw == "" {
		return ""
	}
	if hash, err := chainverify.NormalizeTxHash(raw); err == nil {
		return hash
	}
	const prefix = "epusdt:"
	if strings.HasPrefix(strings.ToLower(raw), "0x") || len(raw)+len(prefix) > 512 {
		return ""
	}
	return prefix + raw
}

// Unlike general observability logs, this audit is part of the payment commit.
func writePaymentAudit(ctx context.Context, client *dbent.Client, orderID int64, action, operator string, detail map[string]any) error {
	data, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode payment audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(orderID, 10)).
		SetAction(action).SetOperator(operator).SetDetail(string(data)).Save(ctx); err != nil {
		return fmt.Errorf("write payment audit: %w", err)
	}
	return nil
}
