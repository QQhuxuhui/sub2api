package service

import "testing"

func TestOpenAIImageSizeRequiresHighRes(t *testing.T) {
	tests := []struct {
		size string
		want bool
	}{
		{size: "", want: false},
		{size: "auto", want: false},
		{size: "1024x1024", want: false},
		{size: "1024X768", want: false},
		{size: "1k", want: false},
		{size: "1536x1024", want: true},
		{size: "2048x2048", want: true},
		{size: "2k", want: true},
		{size: "2K", want: true},
		{size: "3840x2160", want: true},
		{size: "4k", want: true},
		{size: "4096x4096", want: true},
		{size: "not-a-size", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			if got := openAIImageSizeRequiresHighRes(tt.size); got != tt.want {
				t.Fatalf("openAIImageSizeRequiresHighRes(%q) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}

func TestOpenAIImagesRequestRequiresHighResImage(t *testing.T) {
	var nilReq *OpenAIImagesRequest
	if nilReq.RequiresHighResImage() {
		t.Fatal("nil request should not require high-res image")
	}
	if (&OpenAIImagesRequest{Size: "1024x1024"}).RequiresHighResImage() {
		t.Fatal("1K size should not require high-res image")
	}
	if !(&OpenAIImagesRequest{Size: "4096x4096"}).RequiresHighResImage() {
		t.Fatal("4K size should require high-res image")
	}
}

func TestAccountSupportsOpenAIImagesHighRes(t *testing.T) {
	var nilAccount *Account
	if nilAccount.SupportsOpenAIImagesHighRes() {
		t.Fatal("nil account should not support high-res images")
	}

	tests := []struct {
		name        string
		credentials map[string]any
		want        bool
	}{
		{name: "no credentials", credentials: nil, want: false},
		{name: "flag absent", credentials: map[string]any{"api_key": "sk-x"}, want: false},
		{name: "bool true", credentials: map[string]any{"openai_images_highres": true}, want: true},
		{name: "bool false", credentials: map[string]any{"openai_images_highres": false}, want: false},
		{name: "string true", credentials: map[string]any{"openai_images_highres": "true"}, want: true},
		{name: "string 1", credentials: map[string]any{"openai_images_highres": "1"}, want: true},
		{name: "string false", credentials: map[string]any{"openai_images_highres": "false"}, want: false},
		{name: "number 1", credentials: map[string]any{"openai_images_highres": float64(1)}, want: true},
		{name: "number 0", credentials: map[string]any{"openai_images_highres": float64(0)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: tt.credentials}
			if got := account.SupportsOpenAIImagesHighRes(); got != tt.want {
				t.Fatalf("SupportsOpenAIImagesHighRes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccountSupportsOpenAICapabilitiesRequireHighRes(t *testing.T) {
	plain := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	flagged := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"openai_images_highres": true},
	}

	if accountSupportsOpenAICapabilities(plain, "", OpenAIImagesCapabilityBasic, true) {
		t.Fatal("plain account should be filtered out when high-res is required")
	}
	if !accountSupportsOpenAICapabilities(flagged, "", OpenAIImagesCapabilityBasic, true) {
		t.Fatal("flagged account should pass when high-res is required")
	}
	if !accountSupportsOpenAICapabilities(plain, "", OpenAIImagesCapabilityBasic, false) {
		t.Fatal("plain account should pass when high-res is not required")
	}
	if !accountSupportsOpenAICapabilities(flagged, "", OpenAIImagesCapabilityBasic, false) {
		t.Fatal("flagged account should still serve non-high-res requests")
	}
}
