package domain

// IntentRule sends requests classified as one intent to specific accounts.
// The accounts may belong to any group: intent routing is bound to the group a
// request arrives on, but its targets are plain account IDs.
type IntentRule struct {
	// Name is the label the classifier model answers with. Unique per router.
	Name string `json:"name"`
	// Description tells the classifier model when this intent applies. A rule
	// without one is matched by its keywords only.
	Description string `json:"description"`
	// Keywords route a request to this rule without asking the classifier:
	// any one of them appearing in the user's latest message is a match
	// (case-insensitive substring). Checked on every turn, before the model.
	Keywords   []string `json:"keywords,omitempty"`
	AccountIDs []int64  `json:"account_ids"`
	Enabled    bool     `json:"enabled"`
}

const (
	// IntentClassifierProtocolOpenAIChat posts to {base}/v1/chat/completions.
	IntentClassifierProtocolOpenAIChat = "openai_chat"
	// IntentClassifierProtocolGemini posts to {base}/v1beta/models/{model}:generateContent.
	IntentClassifierProtocolGemini = "gemini"
)
