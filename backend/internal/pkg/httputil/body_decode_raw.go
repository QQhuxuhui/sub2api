package httputil

import "strings"

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
