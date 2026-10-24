package erroridentity

import (
	"strings"
	"testing"
)

func TestCoverageGaps(t *testing.T) {
	s := New([]string{"hidden"})
	before := CoverageStats()
	s.Text("hidden", "")
	s.JSON([]byte(`{"error":`), "public", true)
	s.json([]byte(`{"error":{"message":"hidden"}}`), "public", true, maxDepth)
	s.Text(strings.Repeat("x", MaxPayload+1), "public")
	s.JSON([]byte(`{"unexpected":true}`), "public", true)
	after := CoverageStats()
	if after.MissingModel <= before.MissingModel || after.InvalidJSON <= before.InvalidJSON || after.DepthLimit <= before.DepthLimit || after.SizeLimit <= before.SizeLimit || after.UnknownShape <= before.UnknownShape {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}
