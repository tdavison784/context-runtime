package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// session holds the histories the reasoning probe built, reused by the
// compaction probe so both run on a conversation with real thinking blocks.
type session struct {
	H1 []anthropic.BetaMessageParam // u0, a1(thinking+tool_use), u2(tool_result): open round answered
	H2 []anthropic.BetaMessageParam // H1 + a3 (final answer): closed round
	H3 []anthropic.BetaMessageParam // H2 + u4 + a5(tool_use) + u6(tool_result): second round answered
}

var sessions = map[string]*session{}

// u0Text needs reasoning before the tool call so adaptive thinking emits a
// thinking block ahead of tool_use (the key is "theta": 8 primes below 20).
const u0Text = "First work out the key: it is the lowercase English name of the Greek letter whose 1-based position in the Greek alphabet equals the number of primes below 20. Then call the lookup tool with that key. Afterwards compute (value * 17 + 3) mod 23, showing the arithmetic in one line."

// probeReasoning answers R1-R5 and the FR-CAP-002 edit kinds for one model.
func probeReasoning(ctx context.Context, rec *Recorder, model string, all []string, flagship bool) error {
	q := func(s string) string { return s }
	send := func(id, question string, p anthropic.BetaMessageNewParams) (*anthropic.BetaMessage, *anthropic.Error, error) {
		return rec.Send(ctx, "reasoning/"+model+"/"+id, q(question), p)
	}

	// Turn A: a tool call preceded by thinking. Retry at higher effort if the
	// model skipped thinking (up to xhigh), since every later question needs a thinking block.
	u0 := userText(u0Text)
	var a1 *anthropic.BetaMessage
	for attempt, effort := range []anthropic.BetaOutputConfigEffort{anthropic.BetaOutputConfigEffortMedium, anthropic.BetaOutputConfigEffortHigh, anthropic.BetaOutputConfigEffortXhigh} {
		p := toolParams(model, []anthropic.BetaMessageParam{u0}, "")
		p.OutputConfig.Effort = effort
		m, apiErr, err := send(fmt.Sprintf("A-tool-call-%d", attempt), "setup: first tool round with thinking", p)
		if err != nil {
			return err
		}
		if apiErr != nil {
			return fmt.Errorf("turn A rejected: %s", errorMessage([]byte(apiErr.RawJSON())))
		}
		if countThinking(m.ToParam()) > 0 && toolUseID(m) != "" {
			a1 = m
			if _, err := countTokens(ctx, rec, "count/"+model+"/A", p, billedTotal(m)); err != nil {
				return err
			}
			break
		}
	}
	if a1 == nil {
		return errors.New("no thinking+tool_use turn produced; reasoning questions not determinable")
	}
	a1p := a1.ToParam()
	u2 := toolResult(toolUseID(a1), "41")
	H1 := []anthropic.BetaMessageParam{u0, a1p, u2}

	// R1: replay unchanged, plain and with the binding-controls header (field unset).
	a3, apiErr, err := send("R1-replay-unchanged", "R1 replay prior reasoning unchanged", toolParams(model, H1, ""))
	if err != nil {
		return err
	}
	if apiErr != nil || a3 == nil {
		return errors.New("R1 baseline replay rejected; stopping reasoning group")
	}
	if _, err := countTokens(ctx, rec, "count/"+model+"/R1", toolParams(model, H1, ""), billedTotal(a3)); err != nil {
		return err
	}
	if _, _, err := send("R1-replay-unchanged-header", "R1 replay unchanged; binding header, field unset", toolParams(model, H1, "", betaBinding)); err != nil {
		return err
	}
	H2 := appendMsgs(H1, a3.ToParam())

	// R2: drop the reasoning of the open round, keep tool_use/tool_result.
	noThink := with(H1, 1, stripThinking(a1p))
	if _, _, err := send("R2-drop-reasoning", "R2 drop open-round reasoning (DROP_ALL_REASONING)", toolParams(model, noThink, "")); err != nil {
		return err
	}
	if _, _, err := send("R2-drop-reasoning-dropblock", "R2 drop open-round reasoning under drop_block", toolParams(model, noThink, "drop_block", betaBinding)); err != nil {
		return err
	}

	// R3: tamper with reasoning text and with the signature.
	if thinkingText(a1p) != "" {
		edited := with(H1, 1, editThinking(a1p, func(s string) string { return s + " (edited by probe)" }, id))
		if _, _, err := send("R3-modify-thinking-text", "R3 modify summarized thinking text", toolParams(model, edited, "")); err != nil {
			return err
		}
	} else {
		rec.Note(Observation{ID: "reasoning/" + model + "/R3-modify-thinking-text", Model: model, Note: "thinking text empty under display=summarized; text tamper not applicable"})
	}
	tampered := with(H1, 1, editThinking(a1p, id, flipMiddle))
	if _, _, err := send("R3-tamper-signature", "R3 modify signature", toolParams(model, tampered, "")); err != nil {
		return err
	}
	if _, _, err := send("R3-tamper-signature-dropblock", "R3 modify signature under drop_block", toolParams(model, tampered, "drop_block", betaBinding)); err != nil {
		return err
	}

	// R4: REWRITE an earlier message while replaying later reasoning.
	rewritten := with(H1, 0, userText(u0Text+" Be brief."))
	for _, v := range []struct {
		id, binding string
		betas       []anthropic.AnthropicBeta
		question    string
	}{
		{"R4-rewrite-u0-noheader", "", nil, "R4 edit earlier user message; no header (account enforcement default)"},
		{"R4-rewrite-u0-header-unset", "", []anthropic.AnthropicBeta{betaBinding}, "R4 edit earlier user message; header, field unset (monitor)"},
		{"R4-rewrite-u0-error", "error", []anthropic.AnthropicBeta{betaBinding}, "R4 edit earlier user message; prefix_mismatch_behavior=error"},
		{"R4-rewrite-u0-dropblock", "drop_block", []anthropic.AnthropicBeta{betaBinding}, "R4 edit earlier user message; prefix_mismatch_behavior=drop_block"},
	} {
		if _, _, err := send(v.id, v.question, toolParams(model, rewritten, v.binding, v.betas...)); err != nil {
			return err
		}
	}
	sysEdit := toolParams(model, H1, "drop_block", betaBinding)
	sysEdit.System = []anthropic.BetaTextBlockParam{{Text: probeSystem + " Answer in English."}}
	if _, _, err := send("R4-rewrite-system-dropblock", "R4 edit top-level system prompt; drop_block", sysEdit); err != nil {
		return err
	}
	toolEdit := toolParams(model, H1, "drop_block", betaBinding)
	toolEdit.Tools = []anthropic.BetaToolUnionParam{lookupTool(lookupDescription + " Keys are lowercase.")}
	if _, _, err := send("R4-rewrite-tools-dropblock", "R4 edit a tool description; drop_block", toolEdit); err != nil {
		return err
	}

	// FR-CAP-002 edit kinds expected to be SAFE, each under drop_block so any
	// invalidation is reported in input_transformations instead of failing.
	if _, _, err := send("edit-APPEND-dropblock", "APPEND baseline under drop_block", toolParams(model, H1, "drop_block", betaBinding)); err != nil {
		return err
	}
	withSys := appendMsgs(H1, systemMsg("Operator note: answer in at most 25 words."))
	if _, _, err := send("edit-APPEND_SYSTEM-dropblock", "APPEND_SYSTEM mid-conversation system message", toolParams(model, withSys, "drop_block", betaBinding)); err != nil {
		return err
	}
	cached := with(H1, 0, anthropic.NewBetaUserMessage(anthropic.BetaContentBlockParamUnion{OfText: &anthropic.BetaTextBlockParam{
		Text: u0Text, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}}))
	if _, _, err := send("edit-MOVE_CACHE_MARKERS-dropblock", "MOVE_CACHE_MARKERS add cache_control on earlier block", toolParams(model, cached, "drop_block", betaBinding)); err != nil {
		return err
	}
	deferred := toolParams(model, H1, "drop_block", betaBinding)
	deferred.Tools = append(deferred.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
		Name: "archive_lookup", Description: anthropic.String("Look up archived values."), DeferLoading: anthropic.Bool(true),
		InputSchema: anthropic.BetaToolInputSchemaParam{Properties: map[string]any{"key": map[string]any{"type": "string"}}},
	}})
	if _, _, err := send("edit-ADD_DEFERRED_TOOL-dropblock", "ADD_DEFERRED_TOOL add defer_loading tool", deferred); err != nil {
		return err
	}

	// R5: a new user turn after a closed round. Replay earlier reasoning vs
	// strip it, and compare billed input to see whether it is kept or stripped.
	u4 := userText("Now look up key 'beta' and apply the same formula.")
	withPrior := appendMsgs(H2, u4)
	a5, _, err := send("R5-prior-turn-reasoning-replayed", "R5 earlier-turn reasoning replayed after new user turn", toolParams(model, withPrior, "drop_block", betaBinding))
	if err != nil {
		return err
	}
	if _, err := countTokens(ctx, rec, "count/"+model+"/R5-replayed", toolParams(model, withPrior, "drop_block", betaBinding), billedTotal(a5)); err != nil {
		return err
	}
	stripped := stripAllThinking(withPrior)
	a5s, _, err := send("R5-prior-turn-reasoning-stripped", "R5 earlier-turn reasoning stripped after new user turn", toolParams(model, stripped, "drop_block", betaBinding))
	if err != nil {
		return err
	}
	if a5 != nil && a5s != nil {
		rec.Note(Observation{ID: "reasoning/" + model + "/R5-compare", Model: model,
			Note: fmt.Sprintf("billed input replayed=%d stripped=%d delta=%d (thinking blocks replayed: %d)",
				billedTotal(a5), billedTotal(a5s), billedTotal(a5)-billedTotal(a5s), countThinking(a1p)+countThinking(a3.ToParam()))})
	}

	sess := &session{H1: H1, H2: H2}
	sessions[model] = sess
	if a5 == nil || toolUseID(a5) == "" {
		rec.Note(Observation{ID: "reasoning/" + model + "/H3", Model: model, Note: "second round produced no tool call; DROP_LEADING_REASONING not probed"})
		return nil
	}
	a5p := a5.ToParam()
	H3 := appendMsgs(withPrior, a5p, toolResult(toolUseID(a5), "7"))
	sess.H3 = H3

	// DROP_LEADING_REASONING: remove the oldest thinking block, keep later ones.
	leading := with(H3, 1, stripThinking(a1p))
	if _, _, err := send("edit-DROP_LEADING_REASONING-dropblock", "DROP_LEADING_REASONING remove oldest thinking only", toolParams(model, leading, "drop_block", betaBinding)); err != nil {
		return err
	}
	// Non-leading removal: keep a1's thinking, remove a3's (between two kept blocks).
	if countThinking(a3.ToParam()) > 0 && countThinking(a5p) > 0 {
		middle := with(H3, 3, stripThinking(a3.ToParam()))
		if _, _, err := send("edit-DROP_MIDDLE_REASONING-dropblock", "remove a non-leading thinking block (REWRITE-like)", toolParams(model, middle, "drop_block", betaBinding)); err != nil {
			return err
		}
	}

	// Model binding: replay this model's open round to the other probed model.
	if flagship {
		for _, other := range all[1:] {
			if _, _, err := rec.Send(ctx, "reasoning/"+model+"/model-switch-to-"+other, "model binding: replay blocks on another model",
				toolParams(other, H1, "drop_block", betaBinding)); err != nil {
				return err
			}
		}
	}
	return nil
}
