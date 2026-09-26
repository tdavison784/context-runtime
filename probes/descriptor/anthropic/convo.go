package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// Shared request shapes. Every history edit builds new slices and blocks so a
// variant never mutates the history another probe replays.

const probeSystem = "You are a terse probe assistant. When asked for a key's value, call the lookup tool; never guess values."

var (
	betaBinding   = anthropic.AnthropicBetaThinkingBindingControls2026_08_01
	betaCompact   = anthropic.AnthropicBetaCompact2026_09_04
	betaThreshold = anthropic.AnthropicBetaCompact2026_01_12
	betaContext   = anthropic.AnthropicBetaContextManagement2025_06_27
)

func lookupTool(description string) anthropic.BetaToolUnionParam {
	return anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
		Name:        "lookup",
		Description: anthropic.String(description),
		InputSchema: anthropic.BetaToolInputSchemaParam{
			Properties: map[string]any{"key": map[string]any{"type": "string"}},
			Required:   []string{"key"},
		},
	}}
}

const lookupDescription = "Look up the integer value stored under a key."

// thinkingConfig returns adaptive thinking with summarized display so the
// probe can inspect (and tamper with) thinking text. binding is "", "error" or
// "drop_block"; a non-empty value requires the thinking-binding-controls beta.
func thinkingConfig(binding string) anthropic.BetaThinkingConfigParamUnion {
	a := anthropic.BetaThinkingConfigAdaptiveParam{Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized}
	if binding != "" {
		a.BlockBinding = anthropic.BetaThinkingBlockBindingParam{
			PrefixMismatchBehavior: anthropic.BetaThinkingPrefixMismatchBehavior(binding),
		}
	}
	return anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &a}
}

// toolParams is the reasoning conversation's request: fixed system prompt and
// tool set, adaptive summarized thinking, medium effort.
func toolParams(model string, msgs []anthropic.BetaMessageParam, binding string, betas ...anthropic.AnthropicBeta) anthropic.BetaMessageNewParams {
	return anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(model),
		MaxTokens:    4096,
		System:       []anthropic.BetaTextBlockParam{{Text: probeSystem}},
		Tools:        []anthropic.BetaToolUnionParam{lookupTool(lookupDescription)},
		Thinking:     thinkingConfig(binding),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		Messages:     msgs,
		Betas:        betas,
	}
}

func userText(s string) anthropic.BetaMessageParam {
	return anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(s))
}

func assistantText(s string) anthropic.BetaMessageParam {
	return anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant,
		Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(s)}}
}

func systemMsg(s string) anthropic.BetaMessageParam {
	return anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleSystem,
		Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(s)}}
}

func toolResult(id, content string) anthropic.BetaMessageParam {
	return anthropic.NewBetaUserMessage(anthropic.NewBetaToolResultBlock(id, content, false))
}

func toolUseID(m *anthropic.BetaMessage) string {
	for _, b := range m.Content {
		if tu, ok := b.AsAny().(anthropic.BetaToolUseBlock); ok {
			return tu.ID
		}
	}
	return ""
}

func countThinking(m anthropic.BetaMessageParam) int {
	n := 0
	for _, b := range m.Content {
		if b.OfThinking != nil || b.OfRedactedThinking != nil {
			n++
		}
	}
	return n
}

func thinkingText(m anthropic.BetaMessageParam) string {
	for _, b := range m.Content {
		if b.OfThinking != nil {
			return b.OfThinking.Thinking
		}
	}
	return ""
}

// with returns a copy of msgs with element i replaced.
func with(msgs []anthropic.BetaMessageParam, i int, m anthropic.BetaMessageParam) []anthropic.BetaMessageParam {
	out := append([]anthropic.BetaMessageParam(nil), msgs...)
	out[i] = m
	return out
}

func appendMsgs(msgs []anthropic.BetaMessageParam, more ...anthropic.BetaMessageParam) []anthropic.BetaMessageParam {
	out := append([]anthropic.BetaMessageParam(nil), msgs...)
	return append(out, more...)
}

// mapBlocks rebuilds a message's content. f returns the replacement block and
// whether to keep it.
func mapBlocks(m anthropic.BetaMessageParam, f func(anthropic.BetaContentBlockParamUnion) (anthropic.BetaContentBlockParamUnion, bool)) anthropic.BetaMessageParam {
	out := anthropic.BetaMessageParam{Role: m.Role}
	for _, b := range m.Content {
		if nb, keep := f(b); keep {
			out.Content = append(out.Content, nb)
		}
	}
	return out
}

func stripThinking(m anthropic.BetaMessageParam) anthropic.BetaMessageParam {
	return mapBlocks(m, func(b anthropic.BetaContentBlockParamUnion) (anthropic.BetaContentBlockParamUnion, bool) {
		return b, b.OfThinking == nil && b.OfRedactedThinking == nil
	})
}

func stripAllThinking(msgs []anthropic.BetaMessageParam) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, len(msgs))
	for i, m := range msgs {
		out[i] = stripThinking(m)
	}
	return out
}

func editThinking(m anthropic.BetaMessageParam, text func(string) string, sig func(string) string) anthropic.BetaMessageParam {
	return mapBlocks(m, func(b anthropic.BetaContentBlockParamUnion) (anthropic.BetaContentBlockParamUnion, bool) {
		if b.OfThinking == nil {
			return b, true
		}
		t := *b.OfThinking
		t.Thinking = text(t.Thinking)
		t.Signature = sig(t.Signature)
		return anthropic.BetaContentBlockParamUnion{OfThinking: &t}, true
	})
}

// flipMiddle changes one base64 character in the middle of an opaque value.
func flipMiddle(s string) string {
	if len(s) < 8 {
		return s + "A"
	}
	i := len(s) / 2
	c := byte('A')
	if s[i] == 'A' {
		c = 'B'
	}
	return s[:i] + string(c) + s[i+1:]
}

func id(s string) string { return s }

// countParams mirrors a message request for the token counting endpoint.
func countParams(p anthropic.BetaMessageNewParams) anthropic.BetaMessageCountTokensParams {
	c := anthropic.BetaMessageCountTokensParams{
		Model:             p.Model,
		Messages:          p.Messages,
		Thinking:          p.Thinking,
		OutputConfig:      anthropic.BetaOutputConfigParam{Effort: p.OutputConfig.Effort},
		ContextManagement: p.ContextManagement,
		Betas:             p.Betas,
	}
	if len(p.System) > 0 {
		c.System = anthropic.BetaMessageCountTokensParamsSystemUnion{OfBetaTextBlockArray: p.System}
	}
	for _, t := range p.Tools {
		if t.OfTool != nil {
			c.Tools = append(c.Tools, anthropic.BetaMessageCountTokensParamsToolUnion{OfTool: t.OfTool})
		}
	}
	return c
}

// countTokens records a count_tokens call next to the billed value of the
// same request (FR-PROV-004 counter verification).
func countTokens(ctx context.Context, rec *Recorder, obsID string, p anthropic.BetaMessageNewParams, billed int64) (int64, error) {
	res, err := rec.client.Beta.Messages.CountTokens(ctx, countParams(p))
	if err != nil {
		rec.Note(Observation{ID: obsID, Model: string(p.Model), Error: err.Error()})
		return 0, nil
	}
	rec.Note(Observation{ID: obsID, Model: string(p.Model), Status: 200,
		Note: fmt.Sprintf("count_tokens=%d billed_input_total=%d delta=%d", res.InputTokens, billed, res.InputTokens-billed)})
	return res.InputTokens, nil
}

func billedTotal(m *anthropic.BetaMessage) int64 {
	if m == nil {
		return 0
	}
	return m.Usage.InputTokens + m.Usage.CacheReadInputTokens + m.Usage.CacheCreationInputTokens
}

func transforms(m *anthropic.BetaMessage) string {
	if m == nil {
		return ""
	}
	var s []string
	for _, t := range m.InputTransformations {
		s = append(s, t.RawJSON())
	}
	return "[" + strings.Join(s, ",") + "]"
}
