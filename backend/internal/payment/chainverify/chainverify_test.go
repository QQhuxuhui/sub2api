package chainverify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testHash      = "2a003b114b896199d75998e3712f8cc1f32118ed62ff38419d397282b183c404"
	testReceiver  = "0x4c1349a30c3a91d2cd69329c48dfb02c10d812c7"
	testSender    = "0xeb2d2f1b8c558a40207669291fda468e50c8a0bb"
	bscUSDT       = "0x55d398326f99059ff775485246999027b3197955"
	tronReceiver  = "TP525BEN7X9N1pfkVdc9Pv1W43M2LzU6pe"
	tronUSDTHex   = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	topicTransfer = "0x" + transferTopic
)

func topicFor(addr string) string {
	return "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(addr, "0x")
}

// evmServer answers the three JSON-RPC calls the verifier makes.
func evmServer(t *testing.T, receipt any, head string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		var result any
		switch req.Method {
		case "eth_getTransactionReceipt":
			result = receipt
		case "eth_blockNumber":
			result = head
		case "eth_getBlockByNumber":
			result = map[string]any{"timestamp": "0x6aabcdef"}
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bscReceipt(status string, logs ...map[string]any) map[string]any {
	return map[string]any{"status": status, "blockNumber": "0x64", "logs": logs}
}

func transferLog(contract, from, to, dataHex string) map[string]any {
	return map[string]any{"address": contract, "topics": []string{topicTransfer, topicFor(from), topicFor(to)}, "data": dataHex}
}

func TestEVMTransfersDecodesStablecoinTransfer(t *testing.T) {
	// 4.47 USDT with 18 decimals = 4470000000000000000
	srv := evmServer(t, bscReceipt("0x1",
		transferLog(bscUSDT, testSender, testReceiver, "0x0000000000000000000000000000000000000000000000003e08a15121ff0000"),
		transferLog("0x000000000000000000000000000000000000beef", testSender, testReceiver, "0x01"), // unknown token ignored
	), "0x100")
	v := New(map[string][]string{"bsc": {srv.URL}})

	transfers, err := v.Transfers(context.Background(), "binance", "0x"+strings.ToUpper(testHash))
	require.NoError(t, err)
	require.Len(t, transfers, 1)
	got := transfers[0]
	require.Equal(t, "USDT", got.Token)
	require.Equal(t, "4.47", got.Amount.String())
	require.Equal(t, testSender, got.From)
	require.Equal(t, testReceiver, got.To)
	require.Equal(t, "0x"+testHash, got.TxHash)
	require.EqualValues(t, 0x100-0x64+1, got.Confirmations)
	require.EqualValues(t, 0x6aabcdef, got.BlockTime.Unix())
}

func TestEVMTransfersTerminalStates(t *testing.T) {
	ctx := context.Background()

	reverted := evmServer(t, bscReceipt("0x0"), "0x100")
	_, err := New(map[string][]string{"binance": {reverted.URL}}).Transfers(ctx, "binance", testHash)
	require.ErrorIs(t, err, ErrTxFailed)

	fresh := evmServer(t, bscReceipt("0x1"), "0x65")
	_, err = New(map[string][]string{"binance": {fresh.URL}}).Transfers(ctx, "binance", testHash)
	require.True(t, IsUnconfirmed(err), "%v", err)

	unknown := evmServer(t, nil, "0x100")
	_, err = New(map[string][]string{"binance": {unknown.URL}}).Transfers(ctx, "binance", testHash)
	require.ErrorIs(t, err, ErrTxNotFound)
}

func TestTransfersPrefersNotFoundOverBrokenEndpoint(t *testing.T) {
	unknown := evmServer(t, nil, "0x100")
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(broken.Close)

	_, err := New(map[string][]string{"polygon": {unknown.URL, broken.URL}}).Transfers(context.Background(), "polygon", testHash)
	require.ErrorIs(t, err, ErrTxNotFound, "so callers can go on probing the next network")

	_, err = New(map[string][]string{"polygon": {broken.URL}}).Transfers(context.Background(), "polygon", testHash)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrTxNotFound))
}

func TestTronTransfersDecodesTRC20Transfer(t *testing.T) {
	receiverBytes, err := tronAddressBytes(tronReceiver)
	require.NoError(t, err)
	receiverTopic := strings.Repeat("0", 24) + hexString(receiverBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyid":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": testHash, "blockNumber": 1000, "blockTimeStamp": 1789000000000,
				"receipt": map[string]any{"result": "SUCCESS"},
				"log": []map[string]any{{
					"address": tronUSDTHex,
					"topics":  []string{transferTopic, strings.Repeat("0", 24) + strings.Repeat("ab", 20), receiverTopic},
					"data":    "000000000000000000000000000000000000000000000000000000000016bc50", // 1.49 * 1e6
				}},
			})
		case "/wallet/getnowblock":
			_ = json.NewEncoder(w).Encode(map[string]any{"block_header": map[string]any{"raw_data": map[string]any{"number": 1100}}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	transfers, err := New(map[string][]string{"tron": {srv.URL}}).Transfers(context.Background(), "trc20", testHash)
	require.NoError(t, err)
	require.Len(t, transfers, 1)
	require.Equal(t, "1.49", transfers[0].Amount.String())
	require.Equal(t, tronReceiver, transfers[0].To)
	require.Equal(t, "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", transfers[0].Contract)
	require.EqualValues(t, 101, transfers[0].Confirmations)
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

func TestAddressHelpers(t *testing.T) {
	require.True(t, SameAddress("binance", "0x4C1349A30C3A91D2CD69329C48DFB02C10D812C7", testReceiver))
	require.False(t, SameAddress("binance", testReceiver, testSender))
	require.False(t, SameAddress("binance", "", ""))

	raw, err := tronAddressBytes(tronReceiver)
	require.NoError(t, err)
	require.Equal(t, tronReceiver, tronAddressFromBytes(raw), "base58check round trip")
	require.True(t, SameAddress("tron", tronReceiver, "41"+hexString(raw)))
	require.False(t, SameAddress("tron", tronReceiver, "TP525BEN7X9N1pfkVdc9Pv1W43M2LzU6pf"), "bad checksum is never equal")

	usdt, err := tronAddressBytes("TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	require.NoError(t, err)
	require.Equal(t, tronUSDTHex, hexString(usdt))
}

func TestHashAndNetworkHelpers(t *testing.T) {
	h, err := NormalizeTxHash("  0X" + strings.ToUpper(testHash) + " ")
	require.NoError(t, err)
	require.Equal(t, testHash, h)
	for _, bad := range []string{"", "0x1234", testHash + "00", strings.Repeat("z", 64)} {
		_, err := NormalizeTxHash(bad)
		require.Error(t, err, bad)
	}

	require.Equal(t, []string{"polygon", "binance", "ethereum"}, CandidateNetworks("0x"+testHash, "polygon"))
	require.Equal(t, []string{"tron", "binance", "polygon", "ethereum"}, CandidateNetworks(testHash, "tron"))
	require.Equal(t, []string{"binance", "tron", "polygon", "ethereum"}, CandidateNetworks(testHash, "bsc"))
	require.True(t, SupportedNetwork("BSC"))
	require.False(t, SupportedNetwork("solana"))
}

func TestParseEndpointOverrides(t *testing.T) {
	got, err := ParseEndpointOverrides("bsc=https://a.example/rpc/, tron=https://b.example\nbinance=https://c.example")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{
		"binance": {"https://a.example/rpc", "https://c.example"},
		"tron":    {"https://b.example"},
	}, got)

	for _, bad := range []string{"binance", "solana=https://x.example", "binance=http://plain.example", "binance=//nohost"} {
		_, err := ParseEndpointOverrides(bad)
		require.Error(t, err, bad)
	}
}
