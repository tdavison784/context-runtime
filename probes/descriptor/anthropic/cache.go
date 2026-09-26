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

// filler is deterministic garden-note text. (An earlier ledger filler about
// accounts moving units tripped the cyber refusal classifier on Opus 5.5.) The nonce line makes each run's prefix
// unique so a cache write is observed rather than an earlier run's entry.
func filler(nonce string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Garden notebook %s.\n", nonce)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "Garden note %04d: row %d has %d tulips and %d daisies in bed %c.\n", i, (i*37)%101, (i*13)%97, (i*7)%53, 'A'+rune(i%26))
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

	// sysText reaches a target system size at token granularity: whole filler
	// lines, then single-token words for the remainder.
	sysText := func(nonce string, target int) string {
		lines := linesFor(target)
		rest := target - int(t0) - int(float64(lines)*perLine)
		return filler(nonce, lines) + strings.Repeat(" rose", max(0, rest))
	}

	// C1: bisect the minimum. Each point uses a fresh nonce, so a write (not an
	// earlier entry) decides "cached".
	minTok := docMinCacheable[model]
	if minTok == 0 {
		minTok = 1024
	}
	type point struct {
		target int
		billed int64 // total input tokens billed (uncached + read + write)
		cached bool
	}
	try := func(target int, ttl anthropic.BetaCacheControlEphemeralTTL, tag string) (point, *anthropic.BetaMessage, anthropic.BetaMessageNewParams, error) {
		cc := anthropic.NewBetaCacheControlEphemeralParam()
		if ttl != "" {
			cc.TTL = ttl
		}
		p := cacheParams(model, []anthropic.BetaTextBlockParam{{Text: sysText(run+"-"+tag, target), CacheControl: cc}}, []anthropic.BetaMessageParam{askOK})
		m, _, err := rec.Send(ctx, fmt.Sprintf("%sC1-prefix-%s", pre, tag), fmt.Sprintf("C1 system prefix target ~%d tokens", target), p)
		if err != nil || m == nil {
			return point{}, m, p, err
		}
		return point{target: target, billed: billedTotal(m), cached: m.Usage.CacheCreationInputTokens > 0 || m.Usage.CacheReadInputTokens > 0}, m, p, nil
	}
	lo, _, _, err := try(minTok/2, "", fmt.Sprintf("%d", minTok/2))
	if err != nil {
		return err
	}
	hi, hiMsg, hiParams, err := try(minTok*2+48, "", fmt.Sprintf("%d", minTok*2+48))
	if err != nil {
		return err
	}
	if lo.cached || !hi.cached {
		rec.Note(Observation{ID: pre + "C1-bracket", Model: model,
			Note: fmt.Sprintf("bracket did not hold: %d cached=%v, %d cached=%v", lo.billed, lo.cached, hi.billed, hi.cached)})
		return nil
	}
	for i := 0; hi.target-lo.target > 4 && i < 10; i++ {
		mid := (lo.target + hi.target) / 2
		pt, m, p, err := try(mid, "", fmt.Sprintf("%d", mid))
		if err != nil {
			return err
		}
		if pt.cached {
			hi, hiMsg, hiParams = pt, m, p
		} else {
			lo = pt
		}
	}
	rec.Note(Observation{ID: pre + "C1-bracket", Model: model,
		Note: fmt.Sprintf("largest uncached request: billed input %d; smallest cached: billed input %d (cache write %d, uncached tail %d); documented minimum %d",
			lo.billed, hi.billed, hiMsg.Usage.CacheCreationInputTokens, hiMsg.Usage.InputTokens, minTok)})
	aboveMsg, aboveParams := hiMsg, hiParams
	above := hi

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
	ledger := filler(run+"-C3", linesFor(minTok*2)) + "Remember this notebook."
	u0 := userText(ledger)
	auto := func(msgs ...anthropic.BetaMessageParam) anthropic.BetaMessageNewParams {
		p := cacheParams(model, nil, msgs)
		p.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		return p
	}
	a0 := assistantText("Noted.")
	u1 := userText("How many garden notes are there? Answer with a number.")
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
	if _, _, _, err := try(above.target+8, anthropic.BetaCacheControlEphemeralTTLTTL1h, fmt.Sprintf("%d-ttl1h", above.target+8)); err != nil {
		return err
	}
	return nil
}
