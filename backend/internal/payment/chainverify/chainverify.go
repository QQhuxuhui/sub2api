// Package chainverify reads a confirmed stablecoin transfer straight from the
// chain so an admin can settle a crypto order by transaction hash when the
// gateway could not match it (typically an exchange withdrawal that arrived a
// few cents short of the exact amount the gateway expects).
//
// Only token transfers of the built-in stablecoin contracts are recognized;
// native coin transfers (TRX/BNB/...) are ignored on purpose.
package chainverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	NetworkTron     = "tron"
	NetworkBinance  = "binance"
	NetworkPolygon  = "polygon"
	NetworkEthereum = "ethereum"

	// keccak256("Transfer(address,address,uint256)")
	transferTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	httpTimeout     = 12 * time.Second
	maxResponseSize = 2 << 20
)

var (
	// ErrTxNotFound means no configured endpoint knows the transaction on that network.
	ErrTxNotFound = errors.New("transaction not found")
	// ErrTxFailed means the transaction was mined but reverted.
	ErrTxFailed = errors.New("transaction failed on chain")
)

// Token is a stablecoin contract recognized on a network.
type Token struct {
	Symbol   string
	Contract string
	Decimals int32
}

// knownTokens mirrors the contracts the Epusdt gateway ships with.
var knownTokens = map[string][]Token{
	NetworkTron: {
		{Symbol: "USDT", Contract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Decimals: 6},
	},
	NetworkBinance: {
		{Symbol: "USDT", Contract: "0x55d398326f99059fF775485246999027B3197955", Decimals: 18},
		{Symbol: "USDC", Contract: "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d", Decimals: 18},
	},
	NetworkPolygon: {
		{Symbol: "USDT", Contract: "0xc2132D05D31c914a87C6611C10748AEb04B58e8F", Decimals: 6},
		{Symbol: "USDC", Contract: "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359", Decimals: 6},
		{Symbol: "USDC.E", Contract: "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174", Decimals: 6},
	},
	NetworkEthereum: {
		{Symbol: "USDT", Contract: "0xdAC17F958D2ee523a2206206994597C13D831ec7", Decimals: 6},
		{Symbol: "USDC", Contract: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Decimals: 6},
	},
}

var defaultEndpoints = map[string][]string{
	NetworkTron:     {"https://api.trongrid.io"},
	NetworkBinance:  {"https://bsc-dataseed.bnbchain.org", "https://bsc-rpc.publicnode.com"},
	NetworkPolygon:  {"https://polygon-bor-rpc.publicnode.com", "https://polygon.drpc.org"},
	NetworkEthereum: {"https://ethereum-rpc.publicnode.com", "https://eth.drpc.org"},
}

// minConfirmations keeps a freshly broadcast transaction from being settled
// before it is practically final.
var minConfirmations = map[string]int64{
	NetworkTron:     20,
	NetworkBinance:  5,
	NetworkPolygon:  30,
	NetworkEthereum: 5,
}

// Transfer is one recognized stablecoin transfer inside a transaction.
type Transfer struct {
	Network       string
	TxHash        string
	Token         string
	Contract      string
	From          string
	To            string
	Amount        decimal.Decimal
	BlockTime     time.Time
	Confirmations int64
}

// Verifier fetches transfers from public RPC endpoints, with optional
// per-network overrides.
type Verifier struct {
	client    *http.Client
	endpoints map[string][]string
}

// New builds a verifier. overrides maps network -> endpoint URLs that replace
// the defaults for that network.
func New(overrides map[string][]string) *Verifier {
	endpoints := make(map[string][]string, len(defaultEndpoints))
	for network, urls := range defaultEndpoints {
		endpoints[network] = urls
	}
	for network, urls := range overrides {
		network = NormalizeNetwork(network)
		if _, ok := knownTokens[network]; ok && len(urls) > 0 {
			endpoints[network] = urls
		}
	}
	return &Verifier{client: &http.Client{Timeout: httpTimeout}, endpoints: endpoints}
}

// ParseEndpointOverrides parses "binance=https://a,tron=https://b" (also
// newline/semicolon separated). Only absolute https URLs are accepted.
func ParseEndpointOverrides(raw string) (map[string][]string, error) {
	out := map[string][]string{}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' })
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		name, endpoint, ok := strings.Cut(field, "=")
		if !ok {
			return nil, fmt.Errorf("chain rpc %q must look like network=https://host", field)
		}
		network := NormalizeNetwork(name)
		if _, known := knownTokens[network]; !known {
			return nil, fmt.Errorf("chain rpc: unsupported network %q", strings.TrimSpace(name))
		}
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, fmt.Errorf("chain rpc for %s must be an absolute https URL", network)
		}
		out[network] = append(out[network], endpoint)
	}
	return out, nil
}

// NormalizeNetwork lowercases and maps common aliases to gateway network ids.
func NormalizeNetwork(network string) string {
	switch n := strings.ToLower(strings.TrimSpace(network)); n {
	case "bsc", "bnb", "bep20":
		return NetworkBinance
	case "trx", "trc20":
		return NetworkTron
	case "eth", "erc20":
		return NetworkEthereum
	case "matic":
		return NetworkPolygon
	default:
		return n
	}
}

// SupportedNetwork reports whether transfers can be verified on the network.
func SupportedNetwork(network string) bool {
	_, ok := knownTokens[NormalizeNetwork(network)]
	return ok
}

// NormalizeTxHash returns the canonical lowercase hash without 0x, or an error.
func NormalizeTxHash(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimPrefix(h, "0x")
	if len(h) != 64 {
		return "", fmt.Errorf("transaction hash must be 64 hex characters")
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("transaction hash must be 64 hex characters")
	}
	return h, nil
}

// CandidateNetworks orders the networks worth probing for a hash: the preferred
// one first, then the rest of the same family. A hash pasted with 0x can only
// be EVM; without it both families are possible.
func CandidateNetworks(rawHash, preferred string) []string {
	preferred = NormalizeNetwork(preferred)
	evm := []string{NetworkBinance, NetworkPolygon, NetworkEthereum}
	var pool []string
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawHash)), "0x") {
		pool = evm
	} else {
		pool = append([]string{NetworkTron}, evm...)
	}
	out := make([]string, 0, len(pool))
	for _, n := range pool {
		if n == preferred {
			out = append(out, n)
		}
	}
	for _, n := range pool {
		if n != preferred {
			out = append(out, n)
		}
	}
	return out
}

// SameAddress compares two addresses of a network (EVM is case-insensitive,
// TRON base58 is compared by its decoded bytes).
func SameAddress(network, a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if NormalizeNetwork(network) == NetworkTron {
		ab, errA := tronAddressBytes(a)
		bb, errB := tronAddressBytes(b)
		return errA == nil && errB == nil && bytes.Equal(ab, bb)
	}
	return strings.EqualFold(strings.TrimPrefix(a, "0x"), strings.TrimPrefix(b, "0x"))
}

// Transfers returns every recognized stablecoin transfer of a transaction.
// It fails with ErrTxNotFound / ErrTxFailed, or an "insufficient
// confirmations" error when the transaction is too fresh.
func (v *Verifier) Transfers(ctx context.Context, network, txHash string) ([]Transfer, error) {
	network = NormalizeNetwork(network)
	if _, ok := knownTokens[network]; !ok {
		return nil, fmt.Errorf("unsupported network %q", network)
	}
	hash, err := NormalizeTxHash(txHash)
	if err != nil {
		return nil, err
	}
	var lastErr error
	notFound := false
	for _, endpoint := range v.endpoints[network] {
		var transfers []Transfer
		if network == NetworkTron {
			transfers, err = v.tronTransfers(ctx, endpoint, hash)
		} else {
			transfers, err = v.evmTransfers(ctx, endpoint, network, hash)
		}
		if err == nil {
			return transfers, nil
		}
		// A definitive answer from one endpoint is final; only transport-level
		// problems and "not found" (lagging node) fall through to the next one.
		if errors.Is(err, ErrTxFailed) || errors.Is(err, ErrUnconfirmed) {
			return nil, err
		}
		if errors.Is(err, ErrTxNotFound) {
			notFound = true
			continue
		}
		lastErr = err
	}
	// One healthy endpoint not knowing the hash outweighs another being down:
	// callers probing several networks must be able to move on.
	if notFound {
		return nil, ErrTxNotFound
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no rpc endpoint configured for %s", network)
	}
	return nil, lastErr
}

// ErrUnconfirmed means the transaction is mined but not final yet.
var ErrUnconfirmed = errors.New("insufficient confirmations")

func confirmationError(network string, got int64) error {
	return fmt.Errorf("%w: %d of %d on %s", ErrUnconfirmed, got, minConfirmations[network], network)
}

// IsUnconfirmed reports whether err means "mined but not final yet".
func IsUnconfirmed(err error) bool { return errors.Is(err, ErrUnconfirmed) }

// ---- EVM ----

type evmLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
	Removed bool     `json:"removed"`
}

type evmReceipt struct {
	Status      string   `json:"status"`
	BlockNumber string   `json:"blockNumber"`
	Logs        []evmLog `json:"logs"`
}

func (v *Verifier) evmTransfers(ctx context.Context, endpoint, network, hash string) ([]Transfer, error) {
	var receipt *evmReceipt
	if err := v.evmCall(ctx, endpoint, "eth_getTransactionReceipt", []any{"0x" + hash}, &receipt); err != nil {
		return nil, err
	}
	if receipt == nil || receipt.BlockNumber == "" {
		return nil, ErrTxNotFound
	}
	if receipt.Status != "0x1" {
		return nil, ErrTxFailed
	}
	blockNumber, ok := parseHexBig(receipt.BlockNumber)
	if !ok {
		return nil, fmt.Errorf("evm rpc: bad block number %q", receipt.BlockNumber)
	}

	var head string
	if err := v.evmCall(ctx, endpoint, "eth_blockNumber", []any{}, &head); err != nil {
		return nil, err
	}
	headNumber, ok := parseHexBig(head)
	if !ok {
		return nil, fmt.Errorf("evm rpc: bad head block %q", head)
	}
	confirmations := new(big.Int).Sub(headNumber, blockNumber).Int64() + 1
	if confirmations < minConfirmations[network] {
		return nil, confirmationError(network, confirmations)
	}

	var block struct {
		Timestamp string `json:"timestamp"`
	}
	if err := v.evmCall(ctx, endpoint, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &block); err != nil {
		return nil, err
	}
	ts, ok := parseHexBig(block.Timestamp)
	if !ok {
		return nil, fmt.Errorf("evm rpc: bad block timestamp %q", block.Timestamp)
	}
	blockTime := time.Unix(ts.Int64(), 0)

	var out []Transfer
	for _, l := range receipt.Logs {
		if l.Removed || len(l.Topics) != 3 || strings.TrimPrefix(strings.ToLower(l.Topics[0]), "0x") != transferTopic {
			continue
		}
		token, known := lookupToken(network, l.Address)
		if !known {
			continue
		}
		raw, ok := parseHexBig(l.Data)
		if !ok {
			continue
		}
		out = append(out, Transfer{
			Network:       network,
			TxHash:        "0x" + hash,
			Token:         token.Symbol,
			Contract:      token.Contract,
			From:          topicToEVMAddress(l.Topics[1]),
			To:            topicToEVMAddress(l.Topics[2]),
			Amount:        decimal.NewFromBigInt(raw, -token.Decimals),
			BlockTime:     blockTime,
			Confirmations: confirmations,
		})
	}
	return out, nil
}

func (v *Verifier) evmCall(ctx context.Context, endpoint, method string, params []any, out any) error {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	body, err := v.post(ctx, endpoint, payload)
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("evm rpc %s: decode: %w", method, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("evm rpc %s: %s (%d)", method, envelope.Error.Message, envelope.Error.Code)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

func topicToEVMAddress(topic string) string {
	t := strings.TrimPrefix(strings.ToLower(topic), "0x")
	if len(t) < 40 {
		return ""
	}
	return "0x" + t[len(t)-40:]
}

func parseHexBig(s string) (*big.Int, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
	if s == "" {
		return big.NewInt(0), true
	}
	return new(big.Int).SetString(s, 16)
}

// ---- TRON ----

type tronTxInfo struct {
	ID             string `json:"id"`
	BlockNumber    int64  `json:"blockNumber"`
	BlockTimeStamp int64  `json:"blockTimeStamp"`
	Receipt        struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Result string `json:"result"`
	Log    []struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	} `json:"log"`
}

func (v *Verifier) tronTransfers(ctx context.Context, endpoint, hash string) ([]Transfer, error) {
	payload, _ := json.Marshal(map[string]any{"value": hash})
	body, err := v.post(ctx, endpoint+"/wallet/gettransactioninfobyid", payload)
	if err != nil {
		return nil, err
	}
	var info tronTxInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("tron rpc: decode tx info: %w", err)
	}
	if info.ID == "" || info.BlockNumber == 0 {
		return nil, ErrTxNotFound
	}
	// Contract calls report receipt.result; a top-level result of FAILED marks a revert.
	if strings.EqualFold(info.Result, "FAILED") || (info.Receipt.Result != "" && !strings.EqualFold(info.Receipt.Result, "SUCCESS")) {
		return nil, ErrTxFailed
	}

	headBody, err := v.post(ctx, endpoint+"/wallet/getnowblock", []byte("{}"))
	if err != nil {
		return nil, err
	}
	var head struct {
		BlockHeader struct {
			RawData struct {
				Number int64 `json:"number"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.Unmarshal(headBody, &head); err != nil || head.BlockHeader.RawData.Number == 0 {
		return nil, fmt.Errorf("tron rpc: decode head block")
	}
	confirmations := head.BlockHeader.RawData.Number - info.BlockNumber + 1
	if confirmations < minConfirmations[NetworkTron] {
		return nil, confirmationError(NetworkTron, confirmations)
	}

	var out []Transfer
	for _, l := range info.Log {
		if len(l.Topics) != 3 || strings.ToLower(l.Topics[0]) != transferTopic {
			continue
		}
		contractBytes, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(l.Address), "0x"))
		if err != nil || len(contractBytes) < 20 {
			continue
		}
		contract := tronAddressFromBytes(contractBytes[len(contractBytes)-20:])
		token, known := lookupToken(NetworkTron, contract)
		if !known {
			continue
		}
		raw, ok := parseHexBig(l.Data)
		if !ok {
			continue
		}
		from, errFrom := tronAddressFromTopic(l.Topics[1])
		to, errTo := tronAddressFromTopic(l.Topics[2])
		if errFrom != nil || errTo != nil {
			continue
		}
		out = append(out, Transfer{
			Network:       NetworkTron,
			TxHash:        hash,
			Token:         token.Symbol,
			Contract:      token.Contract,
			From:          from,
			To:            to,
			Amount:        decimal.NewFromBigInt(raw, -token.Decimals),
			BlockTime:     time.UnixMilli(info.BlockTimeStamp),
			Confirmations: confirmations,
		})
	}
	return out, nil
}

func tronAddressFromTopic(topic string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(topic), "0x"))
	if err != nil || len(raw) < 20 {
		return "", fmt.Errorf("bad tron topic")
	}
	return tronAddressFromBytes(raw[len(raw)-20:]), nil
}

// tronAddressFromBytes renders the 20-byte account id as a base58check T-address.
func tronAddressFromBytes(account []byte) string {
	payload := append([]byte{0x41}, account...)
	return base58Encode(append(payload, checksum(payload)...))
}

// tronAddressBytes accepts a base58 T-address or a hex address (with or
// without the 41 prefix) and returns the 20-byte account id.
func tronAddressBytes(addr string) ([]byte, error) {
	addr = strings.TrimSpace(addr)
	if raw, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(addr), "0x")); err == nil {
		switch {
		case len(raw) == 21 && raw[0] == 0x41:
			return raw[1:], nil
		case len(raw) == 20:
			return raw, nil
		}
	}
	decoded, err := base58Decode(addr)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 25 || decoded[0] != 0x41 {
		return nil, fmt.Errorf("not a tron address")
	}
	payload, sum := decoded[:21], decoded[21:]
	if !bytes.Equal(checksum(payload), sum) {
		return nil, fmt.Errorf("tron address checksum mismatch")
	}
	return payload[1:], nil
}

func checksum(payload []byte) []byte {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return second[:4]
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(input []byte) string {
	n := new(big.Int).SetBytes(input)
	radix, mod := big.NewInt(58), new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range input {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	n, radix := new(big.Int), big.NewInt(58)
	for _, r := range s {
		idx := strings.IndexRune(base58Alphabet, r)
		if idx < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", r)
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(idx)))
	}
	decoded := n.Bytes()
	leading := 0
	for leading < len(s) && s[leading] == base58Alphabet[0] {
		leading++
	}
	return append(make([]byte, leading), decoded...), nil
}

// ---- shared ----

func lookupToken(network, contract string) (Token, bool) {
	for _, token := range knownTokens[network] {
		if SameAddress(network, token.Contract, contract) {
			return token, true
		}
	}
	return Token{}, false
}

func (v *Verifier) post(ctx context.Context, endpoint string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chain rpc request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("chain rpc read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chain rpc http %d", resp.StatusCode)
	}
	return body, nil
}
