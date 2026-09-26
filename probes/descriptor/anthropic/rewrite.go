package main

import (
	"context"
	"errors"

	"github.com/anthropics/anthropic-sdk-go"
)

// probeRewrite checks REWRITE as FR-CAP-002 defines it: the edited range and
// everything after it lose their reasoning, while reasoning before the edit
// point is replayed. Every variant sets prefix_mismatch_behavior=error, so any
// invalidated block fails the request instead of being dropped. It builds its
// own session so the committed reasoning fixtures are not rewritten.
func probeRewrite(ctx context.Context, rec *Recorder, model string) error {
	pre := "rewrite/" + model + "/"
	send := func(id, q string, p anthropic.BetaMessageNewParams) (*anthropic.BetaMessage, error) {
		m, apiErr, err := rec.Send(ctx, pre+id, q, p)
		if err == nil && apiErr != nil {
			return nil, errors.New(id + ": " + errorMessage([]byte(apiErr.RawJSON())))
		}
		return m, err
	}

	u0 := userText(u0Text)
	a1, err := send("setup-a1", "setup: thinking + tool_use", toolParams(model, []anthropic.BetaMessageParam{u0}, ""))
	if err != nil {
		return err
	}
	if countThinking(a1.ToParam()) == 0 || toolUseID(a1) == "" {
		return errors.New("setup turn has no thinking + tool_use")
	}
	H1 := []anthropic.BetaMessageParam{u0, a1.ToParam(), toolResult(toolUseID(a1), "41")}
	a3, err := send("setup-a3", "setup: final answer", toolParams(model, H1, ""))
	if err != nil {
		return err
	}
	u4 := userText("Now look up key 'beta' and apply the same formula.")
	H2u4 := appendMsgs(H1, a3.ToParam(), u4)
	a5, err := send("setup-a5", "setup: second tool call", toolParams(model, H2u4, ""))
	if err != nil {
		return err
	}
	if toolUseID(a5) == "" {
		return errors.New("second turn has no tool_use")
	}
	H3 := appendMsgs(H2u4, a5.ToParam(), toolResult(toolUseID(a5), "7"))
	iU4 := len(H1) + 1

	// RW1: rewrite the first message; all later reasoning stripped.
	rw1 := stripAllThinking(with(H3, 0, userText(u0Text+" Be brief.")))
	if _, _, err := rec.Send(ctx, pre+"RW1-rewrite-u0-strip-after", "REWRITE u0, strip all reasoning after it; error mode",
		toolParams(model, rw1, "error", betaBinding)); err != nil {
		return err
	}
	// RW2: rewrite u4; reasoning before it (a1, a3) replayed, after it (a5) stripped.
	rw2 := with(H3, iU4, userText("Now look up key 'beta' and apply the same formula. Be brief."))
	rw2 = with(rw2, iU4+1, stripThinking(a5.ToParam()))
	if _, _, err := rec.Send(ctx, pre+"RW2-rewrite-u4-keep-before-strip-after", "REWRITE u4, replay reasoning before it, strip after; error mode",
		toolParams(model, rw2, "error", betaBinding)); err != nil {
		return err
	}
	// RW3 (control): same edit, but a5's reasoning is replayed.
	if countThinking(a5.ToParam()) > 0 {
		rw3 := with(H3, iU4, userText("Now look up key 'beta' and apply the same formula. Be brief."))
		if _, _, err := rec.Send(ctx, pre+"RW3-rewrite-u4-replay-after", "control: REWRITE u4 but replay reasoning after it; error mode",
			toolParams(model, rw3, "error", betaBinding)); err != nil {
			return err
		}
	} else {
		rec.Note(Observation{ID: pre + "RW3-rewrite-u4-replay-after", Model: model, Note: "a5 carried no thinking; control not applicable"})
	}
	return nil
}
