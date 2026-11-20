package httputil

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeRequestBodyBytes(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"model":"m"}`))
	require.NoError(t, zw.Close())

	out, err := DecodeRequestBodyBytes(" GZIP ", buf.Bytes())
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
