// Package ai contains the Claude-powered features of k8s-guardian: fixing
// findings that have no deterministic fix, right-sizing resources and
// generating custom rules from natural language.
package ai

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is used unless --model or K8S_GUARDIAN_MODEL is set.
const DefaultModel = "claude-opus-5-5"

// Model returns the model to use, honouring K8S_GUARDIAN_MODEL.
func Model(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if m := os.Getenv("K8S_GUARDIAN_MODEL"); m != "" {
		return m
	}
	return DefaultModel
}

// Client calls the Claude API. Credentials are resolved by the Anthropic SDK
// (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or an `ant auth login` profile).
type Client struct {
	client anthropic.Client
	model  string
}

// New creates a Client for the given model ("" = default).
func New(model string) *Client {
	return &Client{client: anthropic.NewClient(), model: Model(model)}
}

// ModelName returns the model the client uses.
func (c *Client) ModelName() string { return c.model }

// request sends a conversation and returns the text of the reply. When schema
// is non-nil the reply is constrained to JSON matching it (structured outputs).
func (c *Client) request(ctx context.Context, system string, msgs []anthropic.MessageParam, schema map[string]any) (string, error) {
	cfg := anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortHigh}
	if schema != nil {
		cfg.Format = anthropic.JSONOutputFormatParam{Schema: schema}
	}
	stream := c.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:        anthropic.Model(c.model),
		MaxTokens:    64000,
		System:       []anthropic.TextBlockParam{{Text: system}},
		OutputConfig: cfg,
		Messages:     msgs,
	},
		// Server-side fallback: if a request is declined by a safety
		// classifier, the API re-serves it on a suitable fallback model.
		option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
		option.WithJSONSet("fallbacks", "default"),
	)
	msg := anthropic.Message{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return "", err
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
			return "", fmt.Errorf("Claude API authentication failed: set ANTHROPIC_API_KEY (or run `ant auth login`): %w", err)
		}
		return "", fmt.Errorf("Claude API: %w", err)
	}
	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		return "", errors.New("Claude declined the request")
	case anthropic.StopReasonMaxTokens:
		return "", errors.New("Claude response was truncated (input too large); try fewer files at once")
	}
	var text string
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text += t.Text
		}
	}
	return text, nil
}

func userText(s string) anthropic.MessageParam {
	return anthropic.NewUserMessage(anthropic.NewTextBlock(s))
}

func strArray() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func object(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}
