package kbase

import (
	"strings"
	"testing"

	"agent-platform/internal/catalog"
)

func TestChunkTextEstimatedTokensSplitsLongEnglishLine(t *testing.T) {
	chunks := chunkText("docs/long.txt", strings.Repeat("abcd", 30), catalog.AgentKBaseChunkConfig{
		Unit:          catalog.AgentKBaseChunkUnitEstimatedTokens,
		MaxTokens:     10,
		OverlapTokens: 0,
	}, "embedding", 1024)

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %#v", len(chunks), chunks)
	}
	for _, chunk := range chunks {
		if got := estimateChunkTokens(chunk.Content); got > 10 {
			t.Fatalf("chunk exceeds estimated token budget: got %d content=%q", got, chunk.Content)
		}
	}
}

func TestChunkTextEstimatedTokensSplitsChineseAndKeepsOverlap(t *testing.T) {
	text := "一二三四五\n六七八九十\n甲乙丙丁戊\n"
	chunks := chunkText("docs/zh.txt", text, catalog.AgentKBaseChunkConfig{
		Unit:          catalog.AgentKBaseChunkUnitEstimatedTokens,
		MaxTokens:     10,
		OverlapTokens: 2,
	}, "embedding", 1024)

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %#v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[1].Content, "九十\n") {
		t.Fatalf("expected second chunk to include estimated-token overlap, got %q", chunks[1].Content)
	}
	for _, chunk := range chunks {
		if got := estimateChunkTokens(chunk.Content); got > 10 {
			t.Fatalf("chunk exceeds estimated token budget: got %d content=%q", got, chunk.Content)
		}
	}
}

func TestChunkTextCharsUsesRunesAndSplitsLongLine(t *testing.T) {
	chunks := chunkText("docs/mixed.txt", "你好世界ABCDE", catalog.AgentKBaseChunkConfig{
		Unit:         catalog.AgentKBaseChunkUnitChars,
		MaxChars:     4,
		OverlapChars: 1,
	}, "embedding", 1024)

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %#v", len(chunks), chunks)
	}
	for _, chunk := range chunks {
		if got := len([]rune(chunk.Content)); got > 4 {
			t.Fatalf("chunk exceeds rune budget: got %d content=%q", got, chunk.Content)
		}
	}
	if chunks[0].Content != "你好世界" || chunks[1].Content != "ABCD" || chunks[2].Content != "D\nE" {
		t.Fatalf("unexpected rune chunks: %#v", []string{chunks[0].Content, chunks[1].Content, chunks[2].Content})
	}
}

func TestChunkTextCapsEstimatedTokenOverlap(t *testing.T) {
	chunks := chunkText("docs/overlap.txt", "一二三四五\n六七八九十\n甲乙丙丁戊\n", catalog.AgentKBaseChunkConfig{
		Unit:          catalog.AgentKBaseChunkUnitEstimatedTokens,
		MaxTokens:     10,
		OverlapTokens: 10,
	}, "embedding", 1024)

	normalized := catalog.NormalizeAgentKBaseChunkConfig(catalog.AgentKBaseChunkConfig{
		Unit:          catalog.AgentKBaseChunkUnitEstimatedTokens,
		MaxTokens:     10,
		OverlapTokens: 10,
	})
	if normalized.OverlapTokens != 2 {
		t.Fatalf("expected overlapTokens cap to 2, got %#v", normalized)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks after capped overlap, got %d: %#v", len(chunks), chunks)
	}
}
