//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAIRemoteCompactionV2Request_MatchesBaseline(t *testing.T) {
	reference := func(body []byte) bool {
		stream, valid := parseOpenAICompatibleStream(body)
		return valid && stream && service.HasCompactionTriggerInInput(body)
	}
	for _, sample := range []string{
		``, `{`, `{}`, `{"stream":true}`, `{"stream":false}`, `{"stream":"true"}`, `{"stream":1}`, `{"stream":null}`,
		`{"stream":true,"input":[]}`,
		`{"stream":true,"input":[{"type":"compaction_trigger"}]}`,
		`{"stream":false,"input":[{"type":"compaction_trigger"}]}`,
		`{"stream":"x","input":[{"type":"compaction_trigger"}]}`,
		`{"input":[{"type":"compaction_trigger"}]}`,
		`{"stream":true,"input":[{"type":"compaction_trigger"}]}`,
		`{"stream":true,"input":[{"type":"message","text":"compaction_trigger"}]}`,
		`{"stream":true,"input":{"type":"compaction_trigger"}}`,
	} {
		require.Equal(t, reference([]byte(sample)), isOpenAIRemoteCompactionV2Request([]byte(sample)), "body: %q", sample)
	}
}
