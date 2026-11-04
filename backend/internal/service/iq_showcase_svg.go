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
			if err := checkIQShowcaseSVGAttrs(t); err != nil {
				return "", err
			}
			if depth == 0 {
				if t.Name.Local != "svg" {
					return "", errors.New("root element is not svg")
				}
				switch t.Name.Space {
				case "":
					// xmlns="" declares "no namespace"; adding ours would
					// duplicate the attribute.
					for _, attr := range t.Attr {
						if attr.Name.Space == "" && attr.Name.Local == "xmlns" {
							return "", errors.New("root svg declares an empty namespace")
						}
					}
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
				// Whatever we changed must still be one well-formed element.
				if err := checkIQShowcaseSVGWellFormed(svg); err != nil {
					return "", err
				}
				if err := checkIQShowcaseSVGPrefixes(svg); err != nil {
					return "", err
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

// findIQShowcaseSVGStart finds the first "<svg" that starts a tag (followed
// by whitespace, ">" or "/"). It searches the original text: case folding can
// change byte lengths (U+212A KELVIN SIGN lowercases to one byte), which would
// shift the offset. The root must be a lowercase svg element anyway.
func findIQShowcaseSVGStart(text string) int {
	from := 0
	for {
		i := strings.Index(text[from:], "<svg")
		if i < 0 {
			return -1
		}
		i += from
		next := i + len("<svg")
		if next >= len(text) {
			return -1
		}
		switch text[next] {
		case ' ', '\t', '\n', '\r', '>', '/':
			return i
		}
		from = next
	}
}

// checkIQShowcaseSVGWellFormed parses the final SVG once more, strictly, so a
// rewrite can never store something a browser would fail to load.
func checkIQShowcaseSVGWellFormed(svg string) error {
	dec := xml.NewDecoder(strings.NewReader(svg))
	dec.Strict = true
	dec.Entity = nil
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("svg is not well-formed xml: %v", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			if err := checkIQShowcaseSVGAttrs(start); err != nil {
				return err
			}
		}
	}
}

// checkIQShowcaseSVGPrefixes rejects namespace prefixes that are not declared
// on the element or an ancestor, such as xlink:href without xmlns:xlink.
// encoding/xml leaves those unresolved even in Strict mode, while browsers
// refuse the image ("unbound prefix"). It reads raw tokens so that prefixes
// are seen as written; the caller has already checked well-formedness.
func checkIQShowcaseSVGPrefixes(svg string) error {
	dec := xml.NewDecoder(strings.NewReader(svg))
	dec.Strict = true
	dec.Entity = nil
	var scopes []map[string]struct{}
	bound := func(prefix string) bool {
		if prefix == "xml" {
			return true // always bound by the XML spec
		}
		for i := len(scopes) - 1; i >= 0; i-- {
			if _, ok := scopes[i][prefix]; ok {
				return true
			}
		}
		return false
	}
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("svg is not well-formed xml: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			declared := map[string]struct{}{}
			for _, attr := range t.Attr {
				if attr.Name.Space == "xmlns" {
					// xmlns:p="" is not allowed in XML 1.0 namespaces and
					// browsers refuse it; skipping it would instead fall back
					// to an ancestor's binding, so it fails outright.
					if strings.TrimSpace(attr.Value) == "" {
						return fmt.Errorf("svg declares namespace prefix %q with an empty URI", attr.Name.Local)
					}
					declared[attr.Name.Local] = struct{}{}
				}
			}
			scopes = append(scopes, declared)
			if t.Name.Space != "" && !bound(t.Name.Space) {
				return fmt.Errorf("svg uses undeclared namespace prefix %q on <%s:%s>", t.Name.Space, t.Name.Space, t.Name.Local)
			}
			for _, attr := range t.Attr {
				p := attr.Name.Space
				if p == "" || p == "xmlns" {
					continue
				}
				if !bound(p) {
					return fmt.Errorf("svg uses undeclared namespace prefix %q in attribute %s:%s", p, p, attr.Name.Local)
				}
			}
		case xml.EndElement:
			if len(scopes) > 0 {
				scopes = scopes[:len(scopes)-1]
			}
		}
	}
}

// checkIQShowcaseSVGAttrs rejects repeated attributes. encoding/xml accepts
// them, but they are a well-formedness error and browsers refuse the image.
func checkIQShowcaseSVGAttrs(el xml.StartElement) error {
	if len(el.Attr) < 2 {
		return nil
	}
	seen := make(map[xml.Name]struct{}, len(el.Attr))
	for _, attr := range el.Attr {
		if _, dup := seen[attr.Name]; dup {
			return fmt.Errorf("svg element <%s> repeats attribute %q", el.Name.Local, attr.Name.Local)
		}
		seen[attr.Name] = struct{}{}
	}
	return nil
}

// addIQShowcaseSVGNamespace adds the SVG namespace to a root element that
// declares none: an SVG loaded as an image does not render without it.
func addIQShowcaseSVGNamespace(svg string) string {
	return svg[:len("<svg")] + ` xmlns="` + iqShowcaseSVGNamespace + `"` + svg[len("<svg"):]
}
