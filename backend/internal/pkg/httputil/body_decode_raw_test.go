package httputil

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func gzipped(t testing.TB, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func zstded(t testing.TB, data []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(data, nil)
}

func TestDecodeRequestBodyBytes(t *testing.T) {
	out, err := DecodeRequestBodyBytes(" GZIP ", gzipped(t, []byte(`{"model":"m"}`)))
	require.NoError(t, err)
	require.Equal(t, `{"model":"m"}`, string(out))

	raw := []byte("plain")
	out, err = DecodeRequestBodyBytes("identity", raw)
	require.NoError(t, err)
	require.Same(t, &raw[0], &out[0], "identity returns the same slice")

	_, err = DecodeRequestBodyBytes("br", raw)
	require.Error(t, err)
	_, err = DecodeRequestBodyBytes("gzip", raw)
	require.Error(t, err)
}

// A TransferBytesBody must read exactly like the same bytes arriving on the
// wire: same body, same header updates, same errors.
func TestTransferBytesBodyMatchesStreamingRead(t *testing.T) {
	payload := []byte(`{"model":"m","input":"hello"}`)
	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"identity", "", payload},
		{"gzip", "gzip", gzipped(t, payload)},
		{"zstd", "zstd", zstded(t, payload)},
		{"broken gzip", "gzip", []byte("not gzip")},
		{"unsupported", "br", payload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newReq := func(body io.Reader) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/x", body)
				if tc.encoding != "" {
					req.Header.Set("Content-Encoding", tc.encoding)
				}
				req.Header.Set("Content-Length", "123")
				return req
			}
			streamReq := newReq(bytes.NewReader(tc.body))
			wantBody, wantErr := ReadRequestBodyWithPrealloc(streamReq)

			transfer := NewTransferBytesBody(tc.body)
			transferReq := newReq(nil)
			transferReq.Body = transfer
			transferReq.ContentLength = streamReq.ContentLength
			if wantErr == nil && tc.encoding != "" {
				transferReq.ContentLength = int64(len(tc.body))
			}
			gotBody, gotErr := ReadRequestBodyWithPrealloc(transferReq)

			require.Equal(t, wantErr == nil, gotErr == nil)
			if wantErr != nil {
				require.Equal(t, wantErr.Error(), gotErr.Error())
				return
			}
			require.Equal(t, wantBody, gotBody)
			require.Equal(t, streamReq.Header, transferReq.Header)
			require.Equal(t, streamReq.ContentLength, transferReq.ContentLength)

			plain, err := io.ReadAll(NewTransferBytesBody(tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.body, plain, "plain readers see the transfer bytes")
		})
	}
}

// A zstd frame declaring a huge window is refused before the decoder
// allocates history for it.
func TestDecodeRequestBodyBytesRejectsOversizedZstdWindow(t *testing.T) {
	// Frame header: magic, descriptor without single-segment flag, window
	// descriptor 0x90 (exponent 28: a 256 MiB window, which the library
	// default of 512 MiB would accept), then an empty raw block.
	frame := make([]byte, 0, 16)
	frame = binary.LittleEndian.AppendUint32(frame, 0xFD2FB528)
	frame = append(frame, 0x00, 0x90)
	frame = append(frame, 0x01, 0x00, 0x00) // last raw block, size 0
	_, err := DecodeRequestBodyBytes("zstd", frame)
	require.Error(t, err)

	ok, err := DecodeRequestBodyBytes("zstd", zstded(t, []byte(`{"a":1}`)))
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, string(ok))
}

// BenchmarkTransferBytesBodyRead compares reading a poorly compressible gzip
// body from the wire with decoding it from transfer bytes already in memory.
func BenchmarkTransferBytesBodyRead(b *testing.B) {
	data := make([]byte, 8<<20)
	_, _ = rand.Read(data)
	compressed := gzipped(b, data)
	b.Run("stream", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(compressed))
			req.Header.Set("Content-Encoding", "gzip")
			if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("transfer_bytes", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			req.Body = NewTransferBytesBody(compressed)
			req.Header.Set("Content-Encoding", "gzip")
			if _, err := ReadRequestBodyWithPrealloc(req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
