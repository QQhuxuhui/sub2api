package domain

// IntentRule sends requests classified as one intent to specific accounts.
// The accounts may belong to any group: intent routing is bound to the group a
// request arrives on, but its targets are plain account IDs.
type IntentRule struct {
	// Name is the label the classifier model answers with. Unique per router.
	Name string `json:"name"`
	// Description tells the classifier model when this intent applies.
	Description string  `json:"description"`
	AccountIDs  []int64 `json:"account_ids"`
	Enabled     bool    `json:"enabled"`
}

const (
	// IntentClassifierProtocolOpenAIChat posts to {base}/v1/chat/completions.
	IntentClassifierProtocolOpenAIChat = "openai_chat"
	// IntentClassifierProtocolGemini posts to {base}/v1beta/models/{model}:generateContent.
	IntentClassifierProtocolGemini = "gemini"
)
