package erroridentity

import "sync/atomic"

// Coverage counts processing gaps, not unique requests. No payload or identity
// strings are stored. Counters span snapshot refreshes for the process lifetime.
type Coverage struct {
	MissingModel uint64 `json:"missing_model"`
	InvalidJSON  uint64 `json:"invalid_json"`
	DepthLimit   uint64 `json:"depth_limit"`
	SizeLimit    uint64 `json:"size_limit"`
	UnknownShape uint64 `json:"unknown_shape"`
}

var coverage struct{ missingModel, invalidJSON, depthLimit, sizeLimit, unknownShape atomic.Uint64 }

func CoverageStats() Coverage {
	return Coverage{coverage.missingModel.Load(), coverage.invalidJSON.Load(), coverage.depthLimit.Load(), coverage.sizeLimit.Load(), coverage.unknownShape.Load()}
}
func RecordSizeLimit() { coverage.sizeLimit.Add(1) }

// RuleHits counts successful field rewrites; index 0 is model replacement and
// indices 1 through 12 correspond to the documented channel rules.
var ruleHits [13]atomic.Uint64

const RuleVersion = "2026-10-23-v1"

func RuleHits() [13]uint64 {
	var result [13]uint64
	for i := range result {
		result[i] = ruleHits[i].Load()
	}
	return result
}
func recordRewrite(rule int, before, after string) string {
	if before != after {
		ruleHits[rule].Add(1)
	}
	return after
}
