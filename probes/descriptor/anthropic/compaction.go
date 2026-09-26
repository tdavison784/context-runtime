package main

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

const compactInstructions = "Summarize this conversation for continuation. Preserve every key looked up, every returned value, every computed result and the user's open request. Do not call tools; respond with the summary text only."

// restoration stands in for the runtime's mandatory items (FR-MAT-005 step 4).
const restoration = "MANDATORY RESTORED CONTEXT: goal G-7 is still open: every value you report must also be given in hexadecimal."

// probeCompaction answers K1-K3 for one model. The flagship additionally runs
// adoption/restoration, tamper, kept-turn thinking, threshold compaction and
// context editing.
func probeCompaction(ctx context.Context, rec *Recorder, model string, flagship bool) error {
	pre := "compaction/" + model + "/"
	sess := sessions[model]
	if sess == nil || sess.H2 == nil {
		rec.Note(Observation{ID: pre + "skipped", Model: model, Note: "no reasoning session; run the reasoning group first"})
		return nil
	}

	// K1: on-demand compaction of the closed conversation, custom instructions.
	p := toolParams(model, sess.H2, "", betaCompact)
	p.Compaction = anthropic.BetaCompactionConfigUnionParam{OfSummarize: &anthropic.BetaSummarizeCompactionParam{
		Instructions: anthropic.String(compactInstructions)}}
	k1, apiErr, err := rec.Send(ctx, pre+"K1-on-demand-summarize", "K1 on-demand compaction with custom instructions", p)
	if err != nil {
		return err
	}
	if apiErr != nil || k1 == nil || k1.StopReason != anthropic.BetaStopReasonCompaction {
		rec.Note(Observation{ID: pre + "K1-no-block", Model: model, Note: "no compaction block returned; adoption steps skipped"})
		return nil
	}
	block := k1.ToParam() // assistant message holding exactly the compaction block
	if !flagship {
		return nil
	}

	// K2: adopt the block, then restore mandatory context before inference —
	// once as user text, once as a mid-conversation system message.
	q := userText("What value did the lookup return, and what was the final result?")
	if _, _, err := rec.Send(ctx, pre+"K2-adopt-restore-user", "K2 adopt block; restoration as user text before next inference",
		toolParams(model, []anthropic.BetaMessageParam{block, userText(restoration + "\n\n" + "What value did the lookup return, and what was the final result?")}, "", betaCompact)); err != nil {
		return err
	}
	if _, _, err := rec.Send(ctx, pre+"K2-adopt-restore-system", "K2 adopt block; restoration as appended system message",
		toolParams(model, []anthropic.BetaMessageParam{block, q, systemMsg(restoration)}, "", betaCompact)); err != nil {
		return err
	}

	// K3: the summary is readable text; is it integrity-protected?
	tampered := mapBlocks(block, func(b anthropic.BetaContentBlockParamUnion) (anthropic.BetaContentBlockParamUnion, bool) {
		if b.OfCompaction == nil {
			return b, true
		}
		c := *b.OfCompaction
		c.Content = anthropic.String(c.Content.Value + " (edited by probe)")
		return anthropic.BetaContentBlockParamUnion{OfCompaction: &c}, true
	})
	if _, _, err := rec.Send(ctx, pre+"K3-tamper-summary", "K3 edit compaction summary text before resending",
		toolParams(model, []anthropic.BetaMessageParam{tampered, q}, "", betaCompact)); err != nil {
		return err
	}

	// Kept turns: the conversation continued (H3) while H2 was summarized.
	// Adopt the block in front of the turns taken since and check whether
	// their thinking still verifies (background / keep-recent compaction).
	if sess.H3 != nil {
		kept := append([]anthropic.BetaMessageParam{block}, sess.H3[len(sess.H2):]...)
		if _, _, err := rec.Send(ctx, pre+"K-kept-turns-thinking-dropblock", "kept turns after on-demand block: thinking still valid?",
			toolParams(model, kept, "drop_block", betaCompact, betaBinding)); err != nil {
			return err
		}
	}

	// Threshold compaction (compact_20260112): minimum trigger, then one real
	// paused compaction followed by restoration before the next inference.
	thresh := func(msgs []anthropic.BetaMessageParam, trigger int64, pause bool) anthropic.BetaMessageNewParams {
		tp := toolParams(model, msgs, "", betaThreshold)
		tp.MaxTokens = 8192 // the summarization iteration shares max_tokens
		tp.ContextManagement = anthropic.BetaContextManagementConfigParam{Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
			OfCompact20260112: &anthropic.BetaCompact20260112EditParam{
				Trigger:              anthropic.BetaInputTokensTriggerParam{Value: trigger},
				PauseAfterCompaction: anthropic.Bool(pause),
				Instructions:         anthropic.String(compactInstructions),
			}}}}
		return tp
	}
	if _, _, err := rec.Send(ctx, pre+"T1-threshold-trigger-1000", "threshold trigger below documented 50000 minimum", thresh(sess.H2, 1000, true)); err != nil {
		return err
	}
	big := filler("threshold-"+time.Now().UTC().Format("20060102T150405"), 1600) // ~51K tokens
	u := userText(big + "\nQuestion: how many garden notes are in bed C? Answer with a number.")
	t2, _, err := rec.Send(ctx, pre+"T2-threshold-pause", "threshold compaction with pause_after_compaction", thresh([]anthropic.BetaMessageParam{u}, 50000, true))
	if err != nil {
		return err
	}
	if t2 != nil && t2.StopReason == anthropic.BetaStopReasonCompaction {
		cont := []anthropic.BetaMessageParam{t2.ToParam(), userText(restoration + "\n\nQuestion: how many garden notes are in bed C? Answer with a number.")}
		if _, _, err := rec.Send(ctx, pre+"T3-threshold-continue-restored", "after paused threshold compaction: restoration appended, then inference",
			thresh(cont, 50000, true)); err != nil {
			return err
		}
	}

	// Context editing: server-side clearing of old tool results and thinking.
	if sess.H3 != nil {
		ce := toolParams(model, sess.H3, "drop_block", betaContext, betaBinding)
		ce.ContextManagement = anthropic.BetaContextManagementConfigParam{Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
			OfClearToolUses20250919: &anthropic.BetaClearToolUses20250919EditParam{
				Trigger: anthropic.BetaClearToolUses20250919EditTriggerUnionParam{OfInputTokens: &anthropic.BetaInputTokensTriggerParam{Value: 100}},
				Keep:    anthropic.BetaToolUsesKeepParam{Value: 1},
			}}}}
		m, _, err := rec.Send(ctx, pre+"CE1-clear-tool-uses", "context editing: clear old tool results (trigger 100, keep 1)", ce)
		if err != nil {
			return err
		}
		if m != nil {
			rec.Note(Observation{ID: pre + "CE1-applied-edits", Model: model, Note: m.JSON.ContextManagement.Raw()})
		}
		ct := toolParams(model, sess.H3, "drop_block", betaContext, betaBinding)
		ct.ContextManagement = anthropic.BetaContextManagementConfigParam{Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
			OfClearThinking20251015: &anthropic.BetaClearThinking20251015EditParam{
				Keep: anthropic.BetaClearThinking20251015EditKeepUnionParam{OfThinkingTurns: &anthropic.BetaThinkingTurnsParam{Value: 1}},
			}}}}
		m, _, err = rec.Send(ctx, pre+"CE2-clear-thinking", "context editing: clear thinking except last turn", ct)
		if err != nil {
			return err
		}
		if m != nil {
			rec.Note(Observation{ID: pre + "CE2-applied-edits", Model: model, Note: fmt.Sprint(m.JSON.ContextManagement.Raw())})
		}
	}
	return nil
}
