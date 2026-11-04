package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	iqShowcaseMaxSVGBytes    = 256 << 10
	iqShowcaseMaxSVGElements = 5000
	iqShowcaseMaxSVGDepth    = 64
	iqShowcaseSVGNamespace   = "http://www.w3.org/2000/svg"
)

// extractIQShowcaseSVG takes the first <svg> element out of a model answer
// (prose and Markdown fences around it are fine) and checks that it is a
// well-formed, bounded SVG document without DTDs, entities or processing
// instructions. The page renders it only through <img src="data:...">, which
// runs no script and loads nothing external; this check keeps what we store
// small and well-formed.
func extractIQShowcaseSVG(text string) (string, error) {
	start := findIQShowcaseSVGStart(text)
	if start < 0 {
		return "", errors.New("no <svg> element in the answer")
	}
	rest := text[start:]
	dec := xml.NewDecoder(strings.NewReader(rest))
	dec.Strict = true
	// Entities other than the five XML ones are an error; no DTD can add more.
	dec.Entity = nil

	depth, elements := 0, 0
	needsNamespace := false
	for {
		offset := dec.InputOffset()
		if offset > iqShowcaseMaxSVGBytes {
			return "", fmt.Errorf("svg is larger than %d bytes", iqShowcaseMaxSVGBytes)
		}
		tok, err := dec.Token()
		if err == io.EOF {
			return "", errors.New("svg is not closed")
		}
		if err != nil {
			return "", fmt.Errorf("svg is not well-formed xml: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local != "svg" {
					return "", errors.New("root element is not svg")
				}
				switch t.Name.Space {
				case "":
					needsNamespace = true
				case iqShowcaseSVGNamespace:
				default:
					return "", fmt.Errorf("root svg is in namespace %q, not %s", t.Name.Space, iqShowcaseSVGNamespace)
				}
			}
			depth++
			elements++
			if depth > iqShowcaseMaxSVGDepth {
				return "", fmt.Errorf("svg nests deeper than %d", iqShowcaseMaxSVGDepth)
			}
			if elements > iqShowcaseMaxSVGElements {
				return "", fmt.Errorf("svg has more than %d elements", iqShowcaseMaxSVGElements)
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				end := int(dec.InputOffset())
				if end > iqShowcaseMaxSVGBytes {
					return "", fmt.Errorf("svg is larger than %d bytes", iqShowcaseMaxSVGBytes)
				}
				svg := rest[:end]
				if needsNamespace {
					svg = addIQShowcaseSVGNamespace(svg)
				}
				return svg, nil
			}
		case xml.Directive:
			return "", errors.New("svg must not contain a DTD or other directives")
		case xml.ProcInst:
			return "", errors.New("svg must not contain processing instructions")
		}
	}
}

// findIQShowcaseSVGStart finds the first "<svg" that starts a tag (followed by
// whitespace, ">" or "/"), case-insensitively.
func findIQShowcaseSVGStart(text string) int {
	lower := strings.ToLower(text)
	from := 0
	for {
		i := strings.Index(lower[from:], "<svg")
		if i < 0 {
			return -1
		}
		i += from
		next := i + len("<svg")
		if next >= len(lower) {
			return -1
		}
		switch lower[next] {
		case ' ', '\t', '\n', '\r', '>', '/':
			return i
		}
		from = next
	}
}

// addIQShowcaseSVGNamespace adds the SVG namespace to a root element that
// declares none: an SVG loaded as an image does not render without it.
func addIQShowcaseSVGNamespace(svg string) string {
	return svg[:len("<svg")] + ` xmlns="` + iqShowcaseSVGNamespace + `"` + svg[len("<svg"):]
}
