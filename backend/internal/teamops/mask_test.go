//go:build unit

package teamops

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MaskKey 是纯函数，所以直接钉住入参→出参，不再绕数据库。
// 集成测试里那两条（k:3 的短分支、生产形态的长分支）钉的是「ListRows 会不会把掩码
// 装进 MaskedKey」这条链路，与这里不重复。
//
// 期望值一律写字面量：拿 MaskKey 自己去断 MaskKey 的话等式恒成立，
// 把函数体换成 `return key`（明文密钥直接下发）都不会红。
func TestMaskKey(t *testing.T) {
	t.Parallel()

	// 生产里真正生成的密钥：sk- + 32 字节的 hex，共 67 个字符
	// （service/api_key_service.go 的 GenerateKey）。
	productionKey := "sk-" + strings.Repeat("0123456789abcdef", 4)
	require.Len(t, productionKey, 67, "这条断言钉住的是「生产密钥有多长」这个前提本身")

	cases := []struct {
		name string
		key  string
		want string
	}{
		// 短于 4 位的分支：JS 的 slice(0, 4) 返回整个字符串，Go 的 runes[:4] 会切进
		// 底层数组的零值区，产出 "abc\x00***"。生产走不到（自定义密钥下限 16 位），
		// 但 MaskKey 是导出函数，调用方不止 ListRows 一处。
		{"空串", "", ""},
		{"1 个字符", "a", "a***"},
		{"3 个字符", "abc", "abc***"},
		{"正好 4 个字符", "abcd", "abcd***"},
		{"多字节短串", "王磊", "王磊***"},

		// 12/13 是长短分支的分界。少了这两条，把 <= 12 改成 <= 11 或 <= 13
		// 全套测试照样绿 —— 边界两侧都得有人站着。
		{"12 个字符走短分支", "sk-abcdefghi", "sk-a***"},
		{"13 个字符走长分支", "sk-abcdefghij", "sk-abc...ghij"},

		{"生产长度的密钥", productionKey, "sk-012...cdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, MaskKey(tc.key))
		})
	}
}
