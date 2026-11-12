package claude

import (
	"regexp"
	"strings"
)

// adaptiveOnlyThinkingFamilies lists the model families where the Messages API
// removed thinking.budget_tokens: {type:"enabled",budget_tokens} returns a 400
// and {type:"adaptive"} is the only way to turn thinking on.
var adaptiveOnlyThinkingFamilies = []string{
	"claude-fable-5",
	"claude-mythos-5",
	"claude-mythos-preview",
	"claude-opus-5",
	"claude-opus-4-8",
	"claude-opus-4-7",
	"claude-sonnet-5",
}

// Vertex service accounts write the snapshot date as @YYYYMMDD.
var vertexSnapshotSuffixRe = regexp.MustCompile(`@\d{8}$`)

// RequiresAdaptiveThinking reports whether the model accepts thinking only as
// {type:"adaptive"}. The 5.5 models are excluded: they are adaptive-only too,
// but the protocol converters give them a dedicated branch.
func RequiresAdaptiveThinking(model string) bool {
	id := normalizeEffortModelID(vertexSnapshotSuffixRe.ReplaceAllString(strings.TrimSpace(model), ""))
	if id == "" {
		return false
	}
	for _, excluded := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		if id == excluded || strings.HasPrefix(id, excluded+"-") {
			return false
		}
	}
	for _, family := range adaptiveOnlyThinkingFamilies {
		if id == family || strings.HasPrefix(id, family+"-") {
			return true
		}
	}
	return false
}
