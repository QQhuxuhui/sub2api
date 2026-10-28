package chainverify

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Incoming-transfer scanning for EVM networks. Public nodes only serve logs
// for recent blocks (publicnode: roughly the last hour on BSC) and some refuse
// eth_getLogs outright, so callers scan forward from a cursor in small ranges
// instead of searching history.

// ErrScanUnsupported means the network cannot be scanned (TRON for now).
var ErrScanUnsupported = errors.New("incoming transfer scan not supported on this network")

// IncomingTransfer is a stablecoin transfer found by a block range scan.
type IncomingTransfer struct {
	Transfer
	BlockNumber uint64
	LogIndex    uint64
}

type evmScanLog struct {
	evmLog
	BlockNumber     string `json:"blockNumber"`
	BlockTimestamp  string `json:"blockTimestamp"`
	TransactionHash string `json:"transactionHash"`
	LogIndex        string `json:"logIndex"`
}

// ScanSupported reports whether ScanIncoming works on network.
func ScanSupported(network string) bool {
	network = NormalizeNetwork(network)
	_, ok := knownTokens[network]
	return ok && network != NetworkTron
}

// MinConfirmations is how deep a block must be before its transfers count.
func MinConfirmations(network string) uint64 {
	return uint64(minConfirmations[NormalizeNetwork(network)])
}

// HeadBlock returns the latest block number of an EVM network.
func (v *Verifier) HeadBlock(ctx context.Context, network string) (uint64, error) {
	network = NormalizeNetwork(network)
	if !ScanSupported(network) {
		return 0, ErrScanUnsupported
	}
	var lastErr error
	for _, endpoint := range v.endpoints[network] {
		var head string
		if err := v.evmCall(ctx, endpoint, "eth_blockNumber", []any{}, &head); err != nil {
			lastErr = err
			continue
		}
		n, ok := parseHexBig(head)
		if !ok || !n.IsUint64() {
			lastErr = fmt.Errorf("evm rpc: bad head block %q", head)
			continue
		}
		return n.Uint64(), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no rpc endpoint configured for %s", network)
	}
	return 0, lastErr
}

// ScanIncoming lists transfers of token into any of addresses within
// [fromBlock, toBlock]. Every endpoint is tried in turn; the first one that
// answers wins.
func (v *Verifier) ScanIncoming(ctx context.Context, network, token string, addresses []string, fromBlock, toBlock uint64) ([]IncomingTransfer, error) {
	network = NormalizeNetwork(network)
	if !ScanSupported(network) {
		return nil, ErrScanUnsupported
	}
	if toBlock < fromBlock || len(addresses) == 0 {
		return nil, nil
	}
	var tok Token
	found := false
	for _, t := range knownTokens[network] {
		if strings.EqualFold(t.Symbol, strings.TrimSpace(token)) {
			tok, found = t, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown token %s on %s", token, network)
	}
	toTopics := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		a := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(addr)), "0x")
		if len(a) != 40 {
			continue
		}
		toTopics = append(toTopics, "0x"+strings.Repeat("0", 24)+a)
	}
	if len(toTopics) == 0 {
		return nil, nil
	}
	filter := map[string]any{
		"fromBlock": fmt.Sprintf("0x%x", fromBlock),
		"toBlock":   fmt.Sprintf("0x%x", toBlock),
		"address":   tok.Contract,
		"topics":    []any{"0x" + transferTopic, nil, toTopics},
	}
	var lastErr error
	for _, endpoint := range v.endpoints[network] {
		var logs []evmScanLog
		if err := v.evmCall(ctx, endpoint, "eth_getLogs", []any{filter}, &logs); err != nil {
			lastErr = err
			continue
		}
		out, err := v.decodeScanLogs(ctx, endpoint, network, tok, logs)
		if err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no rpc endpoint configured for %s", network)
	}
	return nil, lastErr
}

func (v *Verifier) decodeScanLogs(ctx context.Context, endpoint, network string, tok Token, logs []evmScanLog) ([]IncomingTransfer, error) {
	blockTimes := map[string]time.Time{}
	out := make([]IncomingTransfer, 0, len(logs))
	for _, l := range logs {
		if l.Removed || len(l.Topics) != 3 || strings.TrimPrefix(strings.ToLower(l.Topics[0]), "0x") != transferTopic {
			continue
		}
		if !SameAddress(network, l.Address, tok.Contract) {
			continue
		}
		raw, ok := parseHexBig(l.Data)
		if !ok {
			continue
		}
		bn, ok := parseHexBig(l.BlockNumber)
		if !ok || !bn.IsUint64() {
			return nil, fmt.Errorf("evm rpc: bad log block number %q", l.BlockNumber)
		}
		li, _ := parseHexBig(l.LogIndex)
		blockTime, ok := blockTimes[l.BlockNumber]
		if !ok {
			ts, tsOK := parseHexBig(l.BlockTimestamp)
			if !tsOK || l.BlockTimestamp == "" || ts.Sign() == 0 {
				var block struct {
					Timestamp string `json:"timestamp"`
				}
				if err := v.evmCall(ctx, endpoint, "eth_getBlockByNumber", []any{l.BlockNumber, false}, &block); err != nil {
					return nil, err
				}
				if ts, tsOK = parseHexBig(block.Timestamp); !tsOK || ts.Sign() == 0 {
					return nil, fmt.Errorf("evm rpc: bad block timestamp %q", block.Timestamp)
				}
			}
			blockTime = time.Unix(ts.Int64(), 0)
			blockTimes[l.BlockNumber] = blockTime
		}
		hash := strings.ToLower(strings.TrimSpace(l.TransactionHash))
		if !strings.HasPrefix(hash, "0x") {
			hash = "0x" + hash
		}
		logIndex := uint64(0)
		if li != nil && li.IsUint64() {
			logIndex = li.Uint64()
		}
		out = append(out, IncomingTransfer{
			Transfer: Transfer{
				Network:   network,
				TxHash:    hash,
				Token:     tok.Symbol,
				Contract:  tok.Contract,
				From:      topicToEVMAddress(l.Topics[1]),
				To:        topicToEVMAddress(l.Topics[2]),
				Amount:    decimal.NewFromBigInt(new(big.Int).Set(raw), -tok.Decimals),
				BlockTime: blockTime,
			},
			BlockNumber: bn.Uint64(),
			LogIndex:    logIndex,
		})
	}
	return out, nil
}
