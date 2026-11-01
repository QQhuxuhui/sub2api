//go:build unit

package service

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// referenceParseString 是基线 f50c98ac7 的逐字节实现。
func referenceParseString(p *openAIResponsesToolSchemaParser) (int, int, error) {
	if p.pos >= len(p.body) || p.body[p.pos] != '"' {
		return 0, 0, p.syntaxError("expected string")
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case '"':
			p.pos++
			return start, p.pos, nil
		case '\\':
			p.pos++
			if p.pos >= len(p.body) {
				return 0, 0, p.syntaxError("unterminated string escape")
			}
			switch p.body[p.pos] {
			case 'u':
				if p.pos+4 >= len(p.body) {
					return 0, 0, p.syntaxError("short unicode escape")
				}
				for _, digit := range p.body[p.pos+1 : p.pos+5] {
					if !isOpenAIResponsesJSONHexDigit(digit) {
						return 0, 0, p.syntaxError("invalid unicode escape")
					}
				}
				p.pos += 5
				continue
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.pos++
			default:
				return 0, 0, p.syntaxError("invalid string escape")
			}
		default:
			if p.body[p.pos] < 0x20 {
				return 0, 0, p.syntaxError("control character in string")
			}
			p.pos++
		}
	}
	return 0, 0, p.syntaxError("unterminated string")
}

func assertParseStringMatchesReference(t *testing.T, body []byte) {
	t.Helper()
	want := openAIResponsesToolSchemaParser{body: body}
	wantStart, wantEnd, wantErr := referenceParseString(&want)
	got := openAIResponsesToolSchemaParser{body: body}
	gotStart, gotEnd, gotErr := got.parseString()
	require.Equal(t, wantStart, gotStart, "body: %q", body)
	require.Equal(t, wantEnd, gotEnd, "body: %q", body)
	require.Equal(t, want.pos, got.pos, "body: %q", body)
	if wantErr == nil {
		require.NoError(t, gotErr, "body: %q", body)
	} else {
		require.EqualError(t, gotErr, wantErr.Error(), "body: %q", body)
	}
}

func TestParseString_MatchesBaseline(t *testing.T) {
	alphabet := []string{"a", "bcdefgh", `"`, `\`, `\n`, `A`, `\u00`, `\q`, "\x00", "\x1f", " ", "\x7f", "\x80", "\xff", "中", "!", "#", "[", "]"}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		b.WriteByte('"')
		for parts := rng.Intn(24); parts > 0; parts-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		if rng.Intn(3) > 0 {
			b.WriteByte('"')
		}
		assertParseStringMatchesReference(t, []byte(b.String()))
	}
	// 特殊字节落在 8 字节组的每个位置。
	for _, special := range []byte{'"', '\\', 0x00, 0x1f, 0x20, 0x21, 0x5b, 0x5d, 0x7f, 0x80, 0xff} {
		for position := 0; position < 24; position++ {
			body := []byte(`"` + strings.Repeat("x", 24) + `"`)
			body[1+position] = special
			assertParseStringMatchesReference(t, body)
		}
	}
}

func FuzzParseStringMatchesBaseline(f *testing.F) {
	f.Add([]byte(`"abc"`))
	f.Add([]byte(`"abcdefghijklmnopA"`))
	f.Add([]byte("\"abcdefgh\x01\""))
	f.Fuzz(func(t *testing.T, body []byte) {
		assertParseStringMatchesReference(t, body)
		if len(body) > 0 {
			quoted := append([]byte{'"'}, body...)
			assertParseStringMatchesReference(t, quoted)
		}
	})
}
