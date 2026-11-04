//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const iqTestSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`

func TestExtractIQShowcaseSVG_Accepts(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"bare":   {iqTestSVG, iqTestSVG},
		"prose":  {"Here you go:\n" + iqTestSVG + "\nEnjoy!", iqTestSVG},
		"fenced": {"```svg\n" + iqTestSVG + "\n```", iqTestSVG},
		"nested svg and comment with a closing tag": {
			`<svg xmlns="http://www.w3.org/2000/svg"><!-- </svg> --><svg x="1"><rect/></svg><![CDATA[</svg>]]></svg> tail`,
			`<svg xmlns="http://www.w3.org/2000/svg"><!-- </svg> --><svg x="1"><rect/></svg><![CDATA[</svg>]]></svg>`,
		},
		"skips <svgfoo": {"<svgfoo> " + iqTestSVG, iqTestSVG},
		// U+212A lowercases to a one-byte "k"; offsets must stay on the original.
		"multibyte text before": {"\u212A: " + iqTestSVG, iqTestSVG},
		"chinese prose before":  {"好的，这是鹈鹕：\n" + iqTestSVG + "\n完成", iqTestSVG},
		"first of two":          {iqTestSVG + `<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`, iqTestSVG},
		"prefixed root": {
			`<s:svg xmlns:s="http://www.w3.org/2000/svg"><s:rect/></s:svg>`, "",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := extractIQShowcaseSVG(c.in)
			if c.want == "" {
				// <s:svg is not found by the "<svg" search; that is a failure, not a crash.
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestExtractIQShowcaseSVG_AddsMissingNamespace(t *testing.T) {
	got, err := extractIQShowcaseSVG(`<svg viewBox="0 0 1 1"><rect/></svg>`)
	require.NoError(t, err)
	require.Equal(t, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect/></svg>`, got)

	got, err = extractIQShowcaseSVG(`<svg xmlns:xlink="http://www.w3.org/1999/xlink"><rect/></svg>`)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(got, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink=`), got)
}

func TestExtractIQShowcaseSVG_Rejects(t *testing.T) {
	deep := strings.Repeat("<g>", iqShowcaseMaxSVGDepth) + strings.Repeat("</g>", iqShowcaseMaxSVGDepth)
	many := strings.Repeat("<rect/>", iqShowcaseMaxSVGElements)
	big := `<svg xmlns="http://www.w3.org/2000/svg"><text>` + strings.Repeat("a", iqShowcaseMaxSVGBytes) + `</text></svg>`
	cases := map[string]string{
		"no svg":              "I cannot draw that.",
		"unclosed":            `<svg xmlns="http://www.w3.org/2000/svg"><rect/>`,
		"malformed":           `<svg xmlns="http://www.w3.org/2000/svg"><rect></svg>`,
		"doctype inside":      `<svg xmlns="http://www.w3.org/2000/svg"><!DOCTYPE x [<!ENTITY a "b">]><text>&a;</text></svg>`,
		"unknown entity":      `<svg xmlns="http://www.w3.org/2000/svg"><text>&nbsp;</text></svg>`,
		"proc inst":           `<svg xmlns="http://www.w3.org/2000/svg"><?xml-stylesheet href="x"?></svg>`,
		"wrong namespace":     `<svg xmlns="http://example.com/ns"><rect/></svg>`,
		"empty namespace":     `<svg xmlns=""><rect/></svg>`,
		"duplicate attribute": `<svg xmlns="http://www.w3.org/2000/svg" width="1" width="2"/>`,
		"too deep":            `<svg xmlns="http://www.w3.org/2000/svg">` + deep + `</svg>`,
		"too many":            `<svg xmlns="http://www.w3.org/2000/svg">` + many + `</svg>`,
		"too big":             big,
		"cut at end":          `<svg`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := extractIQShowcaseSVG(in)
			require.Error(t, err)
		})
	}
}
