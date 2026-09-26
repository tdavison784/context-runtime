// Command anthropic is a throwaway live probe of the Anthropic capability
// descriptor assumptions (SDD section 11, phase 1; ADR 12): reasoning binding,
// cache reads, compaction protocol and token counting.
//
//	set -a; . ../../.env; set +a; go run ./anthropic -out ../../docs/probes/raw.md
//
// The API key is read from ANTHROPIC_API_KEY by the SDK and is never logged.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func main() {
	out := flag.String("out", "", "write the raw observation report (markdown) here")
	fixtures := flag.String("fixtures", "testdata/anthropic", "directory for sanitized fixtures")
	models := flag.String("models", "claude-opus-5-5,claude-sonnet-5", "comma-separated models to probe; the first is the flagship")
	probes := flag.String("probes", "models,reasoning,cache,compaction", "comma-separated probe groups")
	budget := flag.Float64("budget", 4.5, "stop sending once estimated spend exceeds this many USD")
	flag.Parse()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(2)
	}
	if err := os.MkdirAll(*fixtures, 0o755); err != nil {
		fatal(err)
	}
	rec := &Recorder{
		client: anthropic.NewClient(option.WithMaxRetries(3), option.WithRequestTimeout(5*time.Minute)),
		dir:    *fixtures,
		budget: *budget,
	}
	ctx := context.Background()
	ms := strings.Split(*models, ",")
	want := map[string]bool{}
	for _, p := range strings.Split(*probes, ",") {
		want[strings.TrimSpace(p)] = true
	}

	run := func(name string, f func() error) {
		if !want[name] {
			return
		}
		fmt.Fprintf(os.Stderr, "== %s\n", name)
		if err := f(); err != nil {
			fmt.Fprintf(os.Stderr, "probe group %s stopped: %v\n", name, err)
			rec.Note(Observation{ID: name + "/aborted", Note: err.Error()})
		}
	}
	run("models", func() error { return probeModels(ctx, rec) })
	run("threshold-edit", func() error { return probeThresholdEdit(ctx, rec, ms[0], *fixtures) })
	for i, m := range ms {
		flagship := i == 0
		run("reasoning", func() error { return probeReasoning(ctx, rec, m, ms, flagship) })
		run("cache", func() error { return probeCache(ctx, rec, m, flagship) })
		run("compaction", func() error { return probeCompaction(ctx, rec, m, flagship) })
		run("rewrite", func() error { return probeRewrite(ctx, rec, m) })
	}
	fmt.Fprintf(os.Stderr, "estimated spend: $%.4f\n", rec.Spent())
	if *out != "" {
		if err := writeReport(*out, rec); err != nil {
			fatal(err)
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// probeModels lists models with the compaction beta so capabilities include
// compaction support, and records the descriptor-relevant fields.
func probeModels(ctx context.Context, rec *Recorder) error {
	pager := rec.client.Beta.Models.ListAutoPaging(ctx, anthropic.BetaModelListParams{
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaCompact2026_09_04},
	})
	var rows []map[string]any
	for pager.Next() {
		m := pager.Current()
		var caps any
		_ = json.Unmarshal([]byte(m.Capabilities.RawJSON()), &caps)
		rows = append(rows, map[string]any{
			"id": m.ID, "display_name": m.DisplayName, "created_at": m.CreatedAt,
			"max_input_tokens": m.MaxInputTokens, "max_tokens": m.MaxTokens,
			"allowed_fallback_models": m.AllowedFallbackModels, "capabilities": caps,
		})
		rec.Note(Observation{ID: "models/" + m.ID, Model: m.ID, Status: 200,
			Note: fmt.Sprintf("%s created=%s max_input=%d max_output=%d", m.DisplayName,
				m.CreatedAt.Format("2006-01-02"), m.MaxInputTokens, m.MaxTokens)})
	}
	if err := pager.Err(); err != nil {
		return err
	}
	return rec.writeJSON("models_list", map[string]any{"probe": "models", "models": rows})
}

func writeReport(path string, rec *Recorder) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Anthropic descriptor probe: raw observations\n\nGenerated %s by probes/descriptor/anthropic. Estimated spend $%.4f.\n\n",
		time.Now().UTC().Format(time.RFC3339), rec.Spent())
	obs := append([]Observation(nil), rec.Observations()...)
	b.WriteString("| probe | model | status | stop | in | out | cache w | cache r | blocks / transformations / error / note |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, o := range obs {
		detail := strings.Join(o.Blocks, " ")
		if len(o.Transform) > 0 {
			detail += " transforms=" + strings.Join(o.Transform, ",")
		}
		if o.Error != "" {
			detail += " error=" + o.Error
		}
		if o.Note != "" {
			detail += " note=" + o.Note
		}
		if o.Usage.Iterations != "" {
			detail += " iterations=" + o.Usage.Iterations
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %d | %d | %d | %d | %s |\n", mdCell(o.ID), mdCell(o.Model), o.Status, mdCell(o.Stop),
			o.Usage.Input, o.Usage.Output, o.Usage.CacheWrite, o.Usage.CacheRead, mdCell(detail))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	j, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return err
	}
	j, err = Sanitize(j)
	if err != nil {
		return err
	}
	return os.WriteFile(strings.TrimSuffix(path, ".md")+".json", j, 0o644)
}

// mdCell redacts a report cell with the fixture rules (SEC-1.6) and escapes
// it for a markdown table row.
func mdCell(s string) string {
	s = sanitizeString("", s)
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}
