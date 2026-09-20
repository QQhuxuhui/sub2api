package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	"github.com/Wei-Shaw/sub2api/internal/payment/chainverify"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// claimPaymentTransaction must run in the transaction that marks the order
// paid. The unique index arbitrates across processes and payment entry points;
// an upsert must never replace the original owner with the proposed order ID.
func claimPaymentTransaction(ctx context.Context, client *dbent.Client, orderID int64, hash string) error {
	if usedBy, err := paymentTransactionUsedBy(ctx, client, hash, orderID); err != nil {
		return fmt.Errorf("check payment transaction: %w", err)
	} else if usedBy != "" {
		return paymentTransactionAlreadyUsed(usedBy)
	}
	if err := client.PaymentTransactionClaim.Create().SetTxHash(hash).SetOrderID(orderID).
		OnConflictColumns(paymenttransactionclaim.FieldTxHash).Ignore().Exec(ctx); err != nil {
		return fmt.Errorf("claim payment transaction: %w", err)
	}
	claim, err := client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(hash)).Only(ctx)
	if err != nil {
		return fmt.Errorf("read payment transaction claim: %w", err)
	}
	if claim.OrderID != orderID {
		return paymentTransactionAlreadyUsed(strconv.FormatInt(claim.OrderID, 10))
	}
	return nil
}

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
