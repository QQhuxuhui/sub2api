package httputil

import (
	"bytes"
	"errors"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// referenceNormalizeLenient 是基线 4d58662ec 的逐字节实现。
func referenceNormalizeLenient(body []byte, maxNormalizedBytes int64) ([]byte, error) {
	if maxNormalizedBytes <= 0 {
		maxNormalizedBytes = maxDecompressedBodySize
	}
	body = trimUTF8BOM(body)
	if len(body) == 0 {
		return body, nil
	}
	if int64(len(body)) > maxNormalizedBytes {
		return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
	}
	var out []byte
	inString := false
	escaped := false
	for i, b := range body {
		if inString && isJSONControlByte(b) {
			if out == nil {
				capHint := len(body) + 6
				if int64(capHint) > maxNormalizedBytes {
					capHint = int(maxNormalizedBytes)
				}
				out = make([]byte, 0, capHint)
				out = append(out, body[:i]...)
			}
			if int64(len(out)+6) > maxNormalizedBytes {
				return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
			}
			out = appendJSONUnicodeEscape(out, b)
			escaped = false
			continue
		}
		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		}
		if out != nil {
			if int64(len(out)+1) > maxNormalizedBytes {
				return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
			}
			out = append(out, b)
		}
	}
	if out != nil {
		return out, nil
	}
	return body, nil
}

func assertLenientMatchesReference(t *testing.T, body []byte, limit int64) {
	t.Helper()
	wantBody, wantErr := referenceNormalizeLenient(body, limit)
	gotBody, gotErr := NormalizeLenientJSONRequestBody(body, limit)
	require.Equal(t, wantBody, gotBody, "body: %q limit %d", body, limit)
	var wantMax, gotMax *http.MaxBytesError
	require.Equal(t, errors.As(wantErr, &wantMax), errors.As(gotErr, &gotMax), "body: %q", body)
	if wantErr == nil {
		require.NoError(t, gotErr)
	} else {
		require.EqualError(t, gotErr, wantErr.Error())
	}
}

func TestNormalizeLenient_MatchesBaselineRandom(t *testing.T) {
	alphabet := []string{"a", "bcdefgh", `"`, `\`, `\"`, `\\`, `\n`, `A`, "\n", "\t", "\r", "\x00", "\x1f", "\x7f", "\x80", "\xff", "中", "{", "}", ":", ",", " ", strings.Repeat("x", 9), strings.Repeat("y", 24)}
	rng := rand.New(rand.NewSource(13))
	for i := 0; i < 40000; i++ {
		var b strings.Builder
		if rng.Intn(10) == 0 {
			b.WriteString("\xef\xbb\xbf")
		}
		for parts := rng.Intn(30); parts > 0; parts-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		limit := int64(0)
		if rng.Intn(4) == 0 {
			limit = int64(rng.Intn(80) + 1)
		}
		assertLenientMatchesReference(t, []byte(b.String()), limit)
	}
}

func TestNormalizeLenient_SpecialByteAtEveryPosition(t *testing.T) {
	for _, special := range []byte{'"', '\\', 0x00, 0x09, 0x0a, 0x1f, 0x20, 0x21, 0x5b, 0x5d, 0x7e, 0x7f, 0x80, 0xff} {
		for position := 0; position < 40; position++ {
			inner := bytes.Repeat([]byte("x"), 40)
			inner[position] = special
			assertLenientMatchesReference(t, append(append([]byte(`{"a":"`), inner...), `"}`...), 0)
			assertLenientMatchesReference(t, append([]byte(`{"a":"`), inner...), 0)
		}
	}
}

func TestSkipPlainJSONStringBytes(t *testing.T) {
	plain := bytes.Repeat([]byte("abcdefgh"), 4)
	require.Equal(t, 32, skipPlainJSONStringBytes(plain, 0))
	require.Equal(t, 35, skipPlainJSONStringBytes(append(plain, "abc"...), 3), "不足 8 字节的尾部留给逐字节处理")
	for _, special := range []byte{'"', '\\', 0x00, 0x1f, 0x7f} {
		body := bytes.Repeat([]byte("x"), 32)
		body[20] = special
		require.Equal(t, 16, skipPlainJSONStringBytes(body, 0), "byte %x", special)
	}
	require.Equal(t, 32, skipPlainJSONStringBytes(bytes.Repeat([]byte{0x80}, 32), 0), "非 ASCII 不算特殊字节")
}

func FuzzNormalizeLenientMatchesBaseline(f *testing.F) {
	for _, s := range []string{`{}`, `{"a":"b"}`, "{\"a\":\"b\nc\"}", `{"a":"\"`, "\xef\xbb\xbf{\"a\":\"\x7f\"}", `"\`, strings.Repeat("\"x", 12)} {
		f.Add([]byte(s), int64(0))
	}
	f.Fuzz(func(t *testing.T, body []byte, limit int64) {
		if limit < 0 || limit > 1<<20 {
			limit = 0
		}
		assertLenientMatchesReference(t, body, limit)
	})
}

func BenchmarkNormalizeLenient(b *testing.B) {
	body := append(append([]byte(`{"input":"`), bytes.Repeat([]byte("QUJD"), 750000)...), `"}`...)
	b.Run("baseline", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		for b.Loop() {
			_, _ = referenceNormalizeLenient(body, 0)
		}
	})
	b.Run("optimized", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		for b.Loop() {
			_, _ = NormalizeLenientJSONRequestBody(body, 0)
		}
	})
}
