package service

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyImageBillingTier(t *testing.T) {
	tests := []struct {
		name     string
		size     string
		wantTier string
		wantOK   bool
	}{
		{name: "explicit 2k square", size: "2048x2048", wantTier: "2K", wantOK: true},
		{name: "explicit 2k landscape", size: "2048x1152", wantTier: "2K", wantOK: true},
		{name: "explicit 4k landscape", size: "3840x2160", wantTier: "4K", wantOK: true},
		{name: "explicit 4k portrait", size: "2160x3840", wantTier: "4K", wantOK: true},
		{name: "area fallback 1k", size: "1024X768", wantTier: "1K", wantOK: true},
		{name: "area fallback small landscape 1k", size: "1280x768", wantTier: "1K", wantOK: true},
		{name: "area fallback 16:10 2k", size: "2560x1600", wantTier: "2K", wantOK: true},
		{name: "table 1k 16:9", size: "1280x720", wantTier: "1K", wantOK: true},
		{name: "table 1k 16:9 portrait", size: "720x1280", wantTier: "1K", wantOK: true},
		{name: "table 2k 4:3", size: "2304x1728", wantTier: "2K", wantOK: true},
		{name: "table 2k 4:3 portrait", size: "1728x2304", wantTier: "2K", wantOK: true},
		{name: "table 2k 16:9", size: "2560x1440", wantTier: "2K", wantOK: true},
		{name: "table 2k 16:9 portrait", size: "1440x2560", wantTier: "2K", wantOK: true},
		{name: "table 2k 21:9", size: "3024x1296", wantTier: "2K", wantOK: true},
		{name: "area fallback 21:9 portrait 2k", size: "1296x3024", wantTier: "2K", wantOK: true},
		{name: "table 4k square", size: "2880x2880", wantTier: "4K", wantOK: true},
		{name: "table 4k 3:2", size: "3504x2336", wantTier: "4K", wantOK: true},
		{name: "area fallback 4k", size: "4096x4096", wantTier: "4K", wantOK: true},
		{name: "tier string 1k", size: "1k", wantTier: "1K", wantOK: true},
		{name: "empty", size: "", wantOK: false},
		{name: "auto", size: "auto", wantOK: false},
		{name: "invalid", size: "not-a-size", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTier, gotOK := ClassifyImageBillingTier(tt.size)
			require.Equal(t, tt.wantOK, gotOK)
			require.Equal(t, tt.wantTier, gotTier)
		})
	}
}

func TestClassifyImageBillingTier_LargeDimensionsDoNotOverflow(t *testing.T) {
	width := int(^uint(0)>>1)/2 + 1
	tier, ok := ClassifyImageBillingTier(strconv.Itoa(width) + "x3")

	require.True(t, ok)
	require.Equal(t, ImageBillingSize4K, tier)
}

func TestResolveImageBillingSize(t *testing.T) {
	tests := []struct {
		name          string
		inputSize     string
		outputSizes   []string
		wantBilling   string
		wantOutput    string
		wantSource    string
		wantBreakdown map[string]int
	}{
		{
			name:          "output wins over input",
			inputSize:     "1024x1024",
			outputSizes:   []string{"3840x2160"},
			wantBilling:   "4K",
			wantOutput:    "3840x2160",
			wantSource:    ImageSizeSourceOutput,
			wantBreakdown: map[string]int{"4K": 1},
		},
		{
			name:        "input fallback",
			inputSize:   "1024x1024",
			wantBilling: "1K",
			wantSource:  ImageSizeSourceInput,
		},
		{
			name:        "auto defaults",
			inputSize:   "auto",
			wantBilling: "2K",
			wantSource:  ImageSizeSourceDefault,
		},
		{
			name:        "empty defaults",
			inputSize:   "",
			wantBilling: "2K",
			wantSource:  ImageSizeSourceDefault,
		},
		{
			name:        "invalid defaults",
			inputSize:   "largest",
			wantBilling: "2K",
			wantSource:  ImageSizeSourceDefault,
		},
		{
			name:          "mixed output chooses highest tier",
			inputSize:     "1024x1024",
			outputSizes:   []string{"1024x1024", "3840x2160", "2560x1440"},
			wantBilling:   "4K",
			wantOutput:    "1024x1024",
			wantSource:    ImageSizeSourceOutput,
			wantBreakdown: map[string]int{"1K": 1, "2K": 1, "4K": 1},
		},
		{
			name:        "unparseable output falls back to parseable input",
			inputSize:   "2048x1152",
			outputSizes: []string{"auto"},
			wantBilling: "2K",
			wantOutput:  "auto",
			wantSource:  ImageSizeSourceInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveImageBillingSize(tt.inputSize, tt.outputSizes)
			require.Equal(t, tt.wantBilling, got.BillingSize)
			require.Equal(t, tt.inputSize, got.InputSize)
			require.Equal(t, tt.wantOutput, got.OutputSize)
			require.Equal(t, tt.wantSource, got.Source)
			require.Equal(t, tt.wantBreakdown, got.Breakdown)
		})
	}
}
