// error-identity-check replays historical CSV exports through the production
// scrubber. It prints aggregate counts and residual record IDs, never raw bodies.
package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/erroridentity"
)

type count struct {
	Before int      `json:"before"`
	After  int      `json:"after"`
	IDs    []string `json:"residual_ids,omitempty"`
}
type summary struct {
	Rows        int               `json:"rows"`
	Changed     int               `json:"changed"`
	Empty       int               `json:"empty_bodies"`
	InvalidJSON int               `json:"non_json_rows"`
	Categories  map[string]*count `json:"categories"`
}

var detectors = map[string]*regexp.Regexp{
	"chatgpt":         regexp.MustCompile(`(?i)chatgpt`),
	"azure":           regexp.MustCompile(`(?i)azure|eastus|westus|x-ms-`),
	"providers":       regexp.MustCompile(`(?i)providers?=`),
	"internal_header": regexp.MustCompile(`(?i)x-openai-internal`),
	"organization":    regexp.MustCompile(`(?i)org-[A-Za-z0-9]{6,}`),
	"codex2api":       regexp.MustCompile(`(?i)codex2api`),
	"sse_preview":     regexp.MustCompile(`invalid SSE data JSON \(len=[0-9]+\):`),
}

func run() error {
	input := flag.String("input", "", "CSV or CSV.gz export with id, status_code, req, body columns")
	targets := flag.String("targets", "", "newline-separated mapping targets snapshot")
	flag.Parse()
	if *input == "" || *targets == "" {
		return fmt.Errorf("both -input and -targets are required")
	}
	names, err := os.ReadFile(*targets)
	if err != nil {
		return err
	}
	scrub := erroridentity.New(strings.Fields(string(names)))
	f, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	var reader io.Reader = f
	if strings.HasSuffix(*input, ".gz") {
		g, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer g.Close()
		reader = g
	}
	csvr := csv.NewReader(reader)
	header, err := csvr.Read()
	if err != nil {
		return err
	}
	cols := map[string]int{}
	for i, k := range header {
		cols[k] = i
	}
	for _, k := range []string{"id", "status_code", "req", "body"} {
		if _, ok := cols[k]; !ok {
			return fmt.Errorf("missing column %s", k)
		}
	}
	output := map[string]*summary{}
	for _, group := range []string{"non_200", "http_200"} {
		v := &summary{Categories: map[string]*count{}}
		v.Categories["mapped_model"] = &count{}
		for k := range detectors {
			v.Categories[k] = &count{}
		}
		output[group] = v
	}
	for {
		row, err := csvr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		status := row[cols["status_code"]]
		group := "non_200"
		if status == "200" {
			group = "http_200"
		}
		stats := output[group]
		stats.Rows++
		raw, model := row[cols["body"]], row[cols["req"]]
		clean := string(scrub.Body([]byte(raw), model, status != "200"))
		if raw != clean {
			stats.Changed++
		}
		if strings.TrimSpace(raw) == "" {
			stats.Empty++
		}
		if !json.Valid([]byte(raw)) {
			stats.InvalidJSON++
		}
		for _, v := range []struct {
			text  string
			after bool
		}{{raw, false}, {clean, true}} {
			// Known public help links are allowed by the design.
			text := strings.ReplaceAll(v.text, "chatgpt.com/cyber", "")
			for key, c := range stats.Categories {
				hit := false
				if key == "mapped_model" {
					hit = scrub.HasMappedModel(text, model)
				} else {
					hit = detectors[key].MatchString(text)
				}
				if hit {
					if v.after {
						c.After++
						if len(c.IDs) < 20 {
							c.IDs = append(c.IDs, row[cols["id"]])
						}
					} else {
						c.Before++
					}
				}
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		RuleVersion   string              `json:"rule_version"`
		TargetsSHA256 string              `json:"targets_sha256"`
		RuleHits      [13]uint64          `json:"rule_hits"`
		Samples       map[string]*summary `json:"samples"`
	}{erroridentity.RuleVersion, fmt.Sprintf("%x", sha256.Sum256(names)), erroridentity.RuleHits(), output})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
