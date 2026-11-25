package openai

import "testing"

// TestUsageFromChatCarriesCachedTokens verifies OpenAI's automatic prefix-cache
// detail is surfaced as a read-cache breakdown, while the prompt count stays the
// total (cached tokens are a subset of prompt_tokens, not an addition).
func TestUsageFromChatCarriesCachedTokens(t *testing.T) {
	got := usageFromChat(&usage{
		PromptTokens:        1000,
		CompletionTokens:    50,
		TotalTokens:         1050,
		PromptTokensDetails: &promptTokensDetails{CachedTokens: 900},
	})
	if got.InputTokens != 1000 || got.OutputTokens != 50 || got.TotalTokens != 1050 {
		t.Fatalf("counts = %+v", got)
	}
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 900 {
		t.Fatalf("cache read = %+v", got.CacheReadTokens)
	}

	// No cache accounting at all: leave the field unset so consumers can tell
	// "not reported" from "reported as zero".
	if got := usageFromChat(&usage{PromptTokens: 10}); got.CacheReadTokens != nil {
		t.Fatalf("absent details should leave the cache field nil, got %d", *got.CacheReadTokens)
	}
	// A details block present with a zero count IS reported (the provider has
	// cache accounting, nothing hit) — distinguishable from absent.
	if got := usageFromChat(&usage{PromptTokens: 10, PromptTokensDetails: &promptTokensDetails{}}); got.CacheReadTokens == nil || *got.CacheReadTokens != 0 {
		t.Fatalf("reported zero cached should set a zero count, got %+v", got.CacheReadTokens)
	}
}

// TestUsageFromChatDeepSeekCacheShape covers the other cache wire shape:
// DeepSeek (and similar servers) report the automatic prefix-cache split as
// top-level prompt_cache_hit_tokens / prompt_cache_miss_tokens rather than
// OpenAI's prompt_tokens_details block. prompt_tokens is their sum.
func TestUsageFromChatDeepSeekCacheShape(t *testing.T) {
	hitTokens, missTokens := 1920, 86
	got := usageFromChat(&usage{
		PromptTokens:          2006,
		CompletionTokens:      300,
		TotalTokens:           2306,
		PromptCacheHitTokens:  &hitTokens,
		PromptCacheMissTokens: &missTokens,
	})
	if got.InputTokens != 2006 || got.CacheReadTokens == nil || *got.CacheReadTokens != 1920 {
		t.Fatalf("usage = %+v", got)
	}

	// A miss-only round (cache cold) reports zero hits: the count is still
	// reported, so the UI can say "0%" instead of staying silent.
	hit, miss := 0, 100
	cold := usageFromChat(&usage{
		PromptTokens:          100,
		CompletionTokens:      10,
		TotalTokens:           110,
		PromptCacheHitTokens:  &hit,
		PromptCacheMissTokens: &miss,
	})
	if cold.CacheReadTokens == nil || *cold.CacheReadTokens != 0 {
		t.Fatalf("cold cache should report zero hits, got %+v", cold.CacheReadTokens)
	}

	// Both shapes present but disagreeing (one stale): the larger count wins.
	hit, stale := 1920, 0
	both := usageFromChat(&usage{
		PromptTokens:          2006,
		TotalTokens:           2006,
		PromptTokensDetails:   &promptTokensDetails{CachedTokens: stale},
		PromptCacheHitTokens:  &hit,
		PromptCacheMissTokens: &missTokens,
	})
	if both.CacheReadTokens == nil || *both.CacheReadTokens != 1920 {
		t.Fatalf("the larger of the two shapes should win, got %+v", both.CacheReadTokens)
	}
}
