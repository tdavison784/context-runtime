package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// price is USD per million tokens (docs.claude.com pricing page, 2026-09-26).
type price struct{ In, Out, Write5m, Write1h, Read float64 }

var prices = map[string]price{
	"claude-opus-5-5":  {In: 4, Out: 20, Write5m: 5, Write1h: 8, Read: 0.20},
	"claude-opus-5":    {In: 5, Out: 25, Write5m: 6.25, Write1h: 10, Read: 0.50},
	"claude-sonnet-5":  {In: 2, Out: 10, Write5m: 2.5, Write1h: 4, Read: 0.20},
	"claude-haiku-4-5": {In: 1, Out: 5, Write5m: 1.25, Write1h: 2, Read: 0.10},
	"claude-fable-5-1": {In: 10, Out: 50, Write5m: 12.5, Write1h: 20, Read: 0.25},
}

// Observation is one probe exchange, summarized for the report.
type Observation struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Model     string   `json:"model"`
	Betas     []string `json:"betas,omitempty"`
	Status    int      `json:"status"`
	Stop      string   `json:"stop_reason,omitempty"`
	Usage     usageSum `json:"usage"`
	Transform []string `json:"input_transformations,omitempty"`
	Blocks    []string `json:"blocks,omitempty"`
	Error     string   `json:"error,omitempty"`
	Note      string   `json:"note,omitempty"`
	CostUSD   float64  `json:"cost_usd"`
}

type usageSum struct {
	Input, Output, CacheWrite, CacheRead, Write1h int64
	Iterations                                    string `json:",omitempty"`
}

// Recorder sends requests, writes sanitized fixtures and tracks spend.
type Recorder struct {
	client anthropic.Client
	dir    string
	budget float64
	mu     sync.Mutex
	spent  float64
	obs    []Observation
}

var errBudget = errors.New("probe budget exhausted")

func (r *Recorder) Spent() float64 { return r.spent }

func (r *Recorder) Observations() []Observation { return r.obs }

// Note appends a free-form observation that did not come from a message call.
func (r *Recorder) Note(o Observation) { r.obs = append(r.obs, o) }

// Send issues a beta Messages request and records it. A non-nil *anthropic.Error
// is an API rejection (itself evidence); other errors are transport failures.
func (r *Recorder) Send(ctx context.Context, id, question string, p anthropic.BetaMessageNewParams) (*anthropic.BetaMessage, *anthropic.Error, error) {
	if r.spent > r.budget {
		return nil, nil, errBudget
	}
	reqBody, err := json.Marshal(p)
	if err != nil {
		return nil, nil, err
	}
	o := Observation{ID: id, Question: question, Model: string(p.Model), Betas: betaNames(p.Betas)}
	msg, err := r.client.Beta.Messages.New(ctx, p)
	var respBody []byte
	var apiErr *anthropic.Error
	switch {
	case err == nil:
		o.Status = 200
		respBody = []byte(msg.RawJSON())
		o.Stop = string(msg.StopReason)
		o.Usage = summarizeUsage(msg.Usage)
		o.CostUSD = cost(string(p.Model), o.Usage)
		// Absent vs [] matters: the array is present only under the
		// thinking-binding-controls beta.
		if raw := msg.JSON.InputTransformations.Raw(); raw != "" {
			o.Transform = []string{raw}
		} else {
			o.Transform = []string{"<absent>"}
		}
		for _, b := range msg.Content {
			o.Blocks = append(o.Blocks, describeBlock(b))
		}
	case errors.As(err, &apiErr):
		o.Status = apiErr.StatusCode
		respBody = []byte(apiErr.RawJSON())
		o.Error = errorMessage(respBody)
	default:
		return nil, nil, fmt.Errorf("%s: %w", id, err)
	}
	r.spent += o.CostUSD
	r.obs = append(r.obs, o)
	if werr := r.writeFixture(id, o, reqBody, respBody); werr != nil {
		return msg, apiErr, werr
	}
	fmt.Fprintf(os.Stderr, "%-44s %-16s %3d %-10s in=%d out=%d cw=%d cr=%d $%.4f (total $%.3f) %s\n",
		id, o.Model, o.Status, o.Stop, o.Usage.Input, o.Usage.Output, o.Usage.CacheWrite, o.Usage.CacheRead,
		o.CostUSD, r.spent, truncate(o.Error+strings.Join(o.Transform, ","), 160))
	return msg, apiErr, nil
}

func (r *Recorder) writeFixture(id string, o Observation, req, resp []byte) error {
	sreq, err := Sanitize(req)
	if err != nil {
		return err
	}
	sresp, err := Sanitize(resp)
	if err != nil {
		return err
	}
	doc := map[string]any{
		"probe":    id,
		"question": o.Question,
		"model":    o.Model,
		"betas":    o.Betas,
		"status":   o.Status,
		"request":  json.RawMessage(sreq),
		"response": json.RawMessage(sresp),
	}
	return r.writeJSON(id, doc)
}

// WriteJSON writes an already-sanitized document as a fixture.
func (r *Recorder) writeJSON(id string, doc any) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	b, err = Sanitize(b)
	if err != nil {
		return err
	}
	name := strings.ReplaceAll(id, "/", "__") + ".json"
	return os.WriteFile(filepath.Join(r.dir, name), b, 0o644)
}

func summarizeUsage(u anthropic.BetaUsage) usageSum {
	s := usageSum{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheWrite: u.CacheCreationInputTokens,
		CacheRead:  u.CacheReadInputTokens,
		Write1h:    u.CacheCreation.Ephemeral1hInputTokens,
	}
	if raw := u.JSON.Iterations.Raw(); raw != "" && raw != "null" {
		s.Iterations = raw
		// Iterations carry the billed totals when compaction ran; the top-level
		// fields then cover only the message iteration.
		var its []struct {
			Type                     string `json:"type"`
			InputTokens              int64  `json:"input_tokens"`
			OutputTokens             int64  `json:"output_tokens"`
			CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
		}
		if json.Unmarshal([]byte(raw), &its) == nil && len(its) > 0 {
			var t usageSum
			for _, it := range its {
				t.Input += it.InputTokens
				t.Output += it.OutputTokens
				t.CacheRead += it.CacheReadInputTokens
				t.CacheWrite += it.CacheCreationInputTokens
			}
			if t.Input+t.Output+t.CacheRead+t.CacheWrite > s.Input+s.Output+s.CacheRead+s.CacheWrite {
				t.Write1h, t.Iterations = s.Write1h, s.Iterations
				s = t
			}
		}
	}
	return s
}

func cost(model string, u usageSum) float64 {
	p, ok := prices[model]
	if !ok {
		p = prices["claude-fable-5-1"] // unknown model: assume the most expensive
	}
	w5 := u.CacheWrite - u.Write1h
	return (float64(u.Input)*p.In + float64(u.Output)*p.Out + float64(w5)*p.Write5m +
		float64(u.Write1h)*p.Write1h + float64(u.CacheRead)*p.Read) / 1e6
}

func describeBlock(b anthropic.BetaContentBlockUnion) string {
	switch v := b.AsAny().(type) {
	case anthropic.BetaThinkingBlock:
		return fmt.Sprintf("thinking(text=%d sig=%d)", utf8.RuneCountInString(v.Thinking), len(v.Signature))
	case anthropic.BetaRedactedThinkingBlock:
		return fmt.Sprintf("redacted_thinking(data=%d)", len(v.Data))
	case anthropic.BetaTextBlock:
		return fmt.Sprintf("text(%q)", truncate(v.Text, 80))
	case anthropic.BetaToolUseBlock:
		return fmt.Sprintf("tool_use(%s %v)", v.Name, v.Input)
	case anthropic.BetaCompactionBlock:
		// Characters, not bytes: summaries contain multi-byte symbols (SPEC-1.4).
		return fmt.Sprintf("compaction(content=%d encrypted=%d sig=%d)", utf8.RuneCountInString(v.Content), len(v.EncryptedContent), len(v.Signature))
	default:
		return b.Type
	}
}

func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Type    string          `json:"type"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return truncate(string(body), 300)
	}
	s := e.Error.Type + ": " + e.Error.Message
	if len(e.Error.Details) > 0 && string(e.Error.Details) != "null" {
		s += " details=" + string(e.Error.Details)
	}
	return s
}

func betaNames(bs []anthropic.AnthropicBeta) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
