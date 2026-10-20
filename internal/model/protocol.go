// Package model implements the first remote ExtractionProvider protocol and
// tolerant candidate parsing. It never owns database state.
package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

const systemPrompt = "Extract durable memories from the current source. Return only a JSON object with a candidates array. " +
	"Use action add, update, merge_evidence, or skip. Never invent a target_memory_id. " +
	"Prefer Chinese memory text when the source is Chinese."

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Temperature    int            `json:"temperature"`
	ResponseFormat responseFormat `json:"response_format"`
	Messages       []chatMessage  `json:"messages"`
}

type sourcePayload struct {
	ID         string          `json:"id"`
	AppID      string          `json:"app_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	ReceivedAt string          `json:"received_at"`
	Content    json.RawMessage `json:"content"`
}

type relatedPayload struct {
	ID         string      `json:"id"`
	Kind       memory.Kind `json:"kind"`
	Title      string      `json:"title"`
	Content    string      `json:"content"`
	Version    int         `json:"version"`
	ObservedAt string      `json:"observed_at"`
	UpdatedAt  string      `json:"updated_at"`
}

type extractionPayload struct {
	CurrentSource   sourcePayload    `json:"current_source"`
	RelatedMemories []relatedPayload `json:"related_memories"`
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func buildChatRequest(modelName string, request ingest.ExtractionRequest) (chatRequest, error) {
	payload := extractionPayload{
		CurrentSource: sourcePayload{
			ID:         request.Source.ID,
			AppID:      request.Source.AppID,
			ExternalID: request.Source.ExternalID,
			ReceivedAt: formatTime(request.Source.CreatedAt),
			Content:    request.Source.Content,
		},
		RelatedMemories: make([]relatedPayload, 0, len(request.RelatedMemories)),
	}
	for _, item := range request.RelatedMemories {
		payload.RelatedMemories = append(payload.RelatedMemories, relatedPayload{
			ID:         item.ID,
			Kind:       item.Kind,
			Title:      item.Title,
			Content:    item.Content,
			Version:    item.Version,
			ObservedAt: formatTime(item.ObservedAt),
			UpdatedAt:  formatTime(item.UpdatedAt),
		})
	}
	userContent, err := json.Marshal(payload)
	if err != nil {
		return chatRequest{}, fmt.Errorf("编码模型输入: %w", err)
	}
	return chatRequest{
		Model:          modelName,
		Temperature:    0,
		ResponseFormat: responseFormat{Type: "json_object"},
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(userContent)},
		},
	}, nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
