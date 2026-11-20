package httputil

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// zstdRequestDecoderOptions bound what one request body can make the zstd
// decoder allocate. Without them a frame may declare a history window of up
// to 512 MiB; no body this gateway accepts needs a window larger than the
// decompressed-size limit itself.
var zstdRequestDecoderOptions = []zstd.DOption{
	zstd.WithDecoderConcurrency(1),
	zstd.WithDecoderMaxWindow(maxDecompressedBodySize),
	zstd.WithDecoderMaxMemory(maxDecompressedBodySize),
}

// DecodeRequestBodyBytes decodes request bytes that are already in memory,
// sent with the given Content-Encoding (zstd, gzip, deflate), under the same
// decompressed-size limit as ReadRequestBodyWithPrealloc. An empty or identity
// encoding returns raw unchanged. Unlike ReadRequestBodyWithPrealloc it does
// not copy raw before decoding.
func DecodeRequestBodyBytes(contentEncoding string, raw []byte) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(contentEncoding))
	if enc == "" || enc == "identity" {
		return raw, nil
	}
	return decompressRequestBody(enc, raw)
}

// TransferBytesBody replays a request body that is already in memory in its
// transfer form (still compressed when the request carries a
// Content-Encoding). Plain readers see the original bytes;
// ReadRequestBodyWithPrealloc decodes them in place instead of copying them
// into a new buffer first.
type TransferBytesBody struct {
	body   []byte
	reader *bytes.Reader
}

// NewTransferBytesBody wraps transfer bytes already read from a request.
func NewTransferBytesBody(body []byte) *TransferBytesBody {
	return &TransferBytesBody{body: body, reader: bytes.NewReader(body)}
}

// Read implements io.Reader.
func (t *TransferBytesBody) Read(p []byte) (int, error) { return t.reader.Read(p) }

// Close implements io.Closer.
func (t *TransferBytesBody) Close() error { return nil }

// Bytes returns the complete transfer bytes.
func (t *TransferBytesBody) Bytes() []byte { return t.body }

// decodeTransferBytes finishes ReadRequestBodyWithPrealloc for a body whose
// transfer bytes are already in memory. It mirrors that function's decoding
// tail, including the header updates after a successful decode; a
// consistency test keeps the two in step.
func decodeTransferBytes(req *http.Request, raw []byte) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		return raw, nil
	}
	decoded, err := decompressRequestBody(enc, raw)
	if err != nil {
		return nil, fmt.Errorf("decode Content-Encoding %q: %w", enc, err)
	}
	req.Header.Del("Content-Encoding")
	req.Header.Del("Content-Length")
	req.ContentLength = int64(len(decoded))
	return decoded, nil
}
