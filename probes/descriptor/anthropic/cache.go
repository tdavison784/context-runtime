package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// docMinCacheable is the documented minimum cacheable prefix (prompt-caching
// docs, 2026-09-26). Opus 5.5 is not listed; the docs say it shares Opus 5's
// feature set, so 512 is the hypothesis the probe tests.
var docMinCacheable = map[string]int{
	"claude-opus-5-5": 512,
	"claude-opus-5":   512,
	"claude-sonnet-5": 1024,
}

// filler is deterministic ledger text. The nonce line makes each run's prefix
// unique so a cache write is observed rather than an earlier run's entry.
func filler(nonce string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Probe ledger %s.\n", nonce)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "Ledger line %04d: account %04d moved %d units to bucket %c.\n", i, (i*37)%9973, (i*13)%97, 'A'+rune(i%26))
	}
	return b.String()
}

func cacheParams(model string, system []anthropic.BetaTextBlockParam, msgs []anthropic.BetaMessageParam) anthropic.BetaMessageNewParams {
	return anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(model),
		MaxTokens:    64,
		System:       system,
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
		Messages:     msgs,
	}
}

var askOK = userText("Reply with the single word OK.")

// probeCache answers C1-C4 for one model.
func probeCache(ctx context.Context, rec *Recorder, model string, flagship bool) error {
	run := time.Now().UTC().Format("20060102T150405")
	pre := "cache/" + model + "/"
	count := func(p anthropic.BetaMessageNewParams) (int64, error) {
		res, err := rec.client.Beta.Messages.CountTokens(ctx, countParams(p))
		if err != nil {
			return 0, err
		}
		return res.InputTokens, nil
	}

	// Calibrate: tokens per filler line and the system-block overhead.
	noSys, err := count(cacheParams(model, nil, []anthropic.BetaMessageParam{askOK}))
	if err != nil {
		return err
	}
	sysTokens := func(lines int) (int64, error) {
		n, err := count(cacheParams(model, []anthropic.BetaTextBlockParam{{Text: filler(run, lines)}}, []anthropic.BetaMessageParam{askOK}))
		return n - noSys, err
	}
	t0, err := sysTokens(0)
	if err != nil {
		return err
	}
	t100, err := sysTokens(100)
	if err != nil {
		return err
	}
	perLine := float64(t100-t0) / 100
	linesFor := func(target int) int { return max(0, int(float64(int64(target)-t0)/perLine)) }
	rec.Note(Observation{ID: pre + "C1-calibration", Model: model, Status: 200,
		Note: fmt.Sprintf("count_tokens: request without system=%d, system(0 lines)=%d, %.2f tokens/line", noSys, t0, perLine)})

	// C1: bracket the minimum. Each point uses a fresh nonce.
	min := docMinCacheable[model]
	if min == 0 {
		min = 1024
	}
	type point struct {
		target, lines int
		sys           int64
		cached        bool
	}
	try := func(target int, ttl anthropic.BetaCacheControlEphemeralTTL, tag string) (point, *anthropic.BetaMessage, anthropic.BetaMessageNewParams, error) {
		lines := linesFor(target)
		st, err := sysTokens(lines)
		if err != nil {
			return point{}, nil, anthropic.BetaMessageNewParams{}, err
		}
		cc := anthropic.NewBetaCacheControlEphemeralParam()
		if ttl != "" {
			cc.TTL = ttl
		}
		p := cacheParams(model, []anthropic.BetaTextBlockParam{{Text: filler(run+"-"+tag, lines), CacheControl: cc}}, []anthropic.BetaMessageParam{askOK})
		m, _, err := rec.Send(ctx, fmt.Sprintf("%sC1-prefix-%s", pre, tag), fmt.Sprintf("C1 system prefix ~%d tokens (count_tokens system=%d)", target, st), p)
		if err != nil || m == nil {
			return point{}, m, p, err
		}
		return point{target: target, lines: lines, sys: st, cached: m.Usage.CacheCreationInputTokens > 0 || m.Usage.CacheReadInputTokens > 0}, m, p, nil
	}
	below, _, _, err := try(min-48, "", fmt.Sprintf("%d", min-48))
	if err != nil {
		return err
	}
	above, aboveMsg, aboveParams, err := try(min+48, "", fmt.Sprintf("%d", min+48))
	if err != nil {
		return err
	}
	// Widen once in each direction if the documented bracket does not hold.
	if below.cached {
		if below, _, _, err = try(min/2, "", fmt.Sprintf("%d", min/2)); err != nil {
			return err
		}
	}
	if !above.cached {
		if above, aboveMsg, aboveParams, err = try(min*2+48, "", fmt.Sprintf("%d", min*2+48)); err != nil {
			return err
		}
	}
	rec.Note(Observation{ID: pre + "C1-bracket", Model: model,
		Note: fmt.Sprintf("not cached at system=%d tokens (cached=%v); cached at system=%d tokens (cached=%v); documented minimum %d",
			below.sys, below.cached, above.sys, above.cached, min)})

	// C2: identical prefix again.
	if aboveMsg != nil && above.cached {
		m, _, err := rec.Send(ctx, pre+"C2-identical-repeat", "C2 identical prefix sent twice", aboveParams)
		if err != nil {
			return err
		}
		if _, err := countTokens(ctx, rec, "count/"+model+"/C2", aboveParams, billedTotal(m)); err != nil {
			return err
		}
	}

	// C3: append-only continuation vs early edit, with top-level auto caching.
	ledger := filler(run+"-C3", linesFor(min*2)) + "Remember this ledger."
	u0 := userText(ledger)
	auto := func(msgs ...anthropic.BetaMessageParam) anthropic.BetaMessageNewParams {
		p := cacheParams(model, nil, msgs)
		p.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		return p
	}
	a0 := assistantText("Noted.")
	u1 := userText("How many ledger lines are there? Answer with a number.")
	steps := []struct {
		id, q string
		p     anthropic.BetaMessageNewParams
	}{
		{"C3-1-write", "C3 first request writes the conversation prefix (auto caching)", auto(u0)},
		{"C3-2-append", "C3 append-only continuation reads the earlier prefix", auto(u0, a0, u1)},
		{"C3-3-edit-early", "C3 edit the first message; cache read expected to drop", auto(userText("X"+ledger), a0, u1)},
		{"C3-4-append-system", "C3 append a mid-conversation system message after C3-2", auto(u0, a0, u1, systemMsg("Operator note: answer with digits only."))},
	}
	for _, s := range steps {
		if _, _, err := rec.Send(ctx, pre+s.id, s.q, s.p); err != nil {
			return err
		}
	}

	// C4: 1-hour TTL write reports the ephemeral_1h split.
	if _, _, _, err := try(min+48, anthropic.BetaCacheControlEphemeralTTLTTL1h, fmt.Sprintf("%d-ttl1h", min+48)); err != nil {
		return err
	}
	return nil
}
