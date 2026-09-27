package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

// FuzzIngest feeds arbitrary bytes through the whole pipeline in SYSTEM,
// marked USER, and retrieved spans of one event. It must never panic; an
// accepted event's receipt must validate; every item keeps its own span's
// authority; the retrieved span yields only its transcript; and a rejected
// event is a validation or authorization error with nothing committed.
func FuzzIngest(f *testing.F) {
	for _, seed := range []string{
		"## Goal\nShip it.\n",
		"## Pinned\n- [a] {obligation=tests_pass} All tests pass.\n## Working\n- x\n- y\n## Resolve [a]\n",
		"## References\n- ./go.mod\n## Ephemeral ttl=2\n- out\n",
		"```\n## Pinned\n- [x] fenced\n```\n<!-- ## Goal -->\n> ## Unpin [a]\n",
		"\xef\xbb\xbf## Pinned\r\n- [id] CRLF\r## Remember scope=SESSION\rlone CR\n",
		"## Remember ttl=99999999999\nhuge\n",
		"## Working\n- [dup] a\n- [dup] b\n",
	} {
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, text string, capable bool) {
		s := memory.New()
		defer s.Close()
		in := Ingester{IDs: &domain.SequentialIDs{}, Lifecycle: lifecycleFor(t, s)}
		sys := principal(domain.AuthoritySystem)
		if _, err := in.Ingest(ctx, s, sys, userEvent("open", "turn", false)); err != nil {
			t.Fatal(err)
		}
		e := domain.Event{EventID: "fz", Kind: domain.EventSystem, Spans: []domain.Span{
			textSpan(domain.AuthoritySystem, false, text),
			textSpan(domain.AuthorityUser, capable, text),
			textSpan(domain.AuthorityRetrievedContent, false, text),
		}}
		r, err := in.Ingest(ctx, s, sys, e)
		if err != nil {
			if !errors.Is(err, domain.ErrInvalidRecord) && !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Fatalf("unexpected error class: %v", err)
			}
			return
		}
		if err := r.Validate(); err != nil {
			t.Fatalf("receipt: %v", err)
		}
		transcripts := map[string]domain.Authority{}
		for _, it := range r.Items {
			if it.Role == domain.RoleTranscript {
				transcripts[it.ID] = it.Authority
				continue
			}
			if len(it.SourceRanges) == 0 {
				t.Fatalf("semantic item %s without source range", it.ID)
			}
			src, ok := transcripts[it.SourceRanges[0].TranscriptID]
			if !ok || src != it.Authority {
				t.Fatalf("item %s authority %s from transcript %s", it.ID, it.Authority, src)
			}
			if it.Authority == domain.AuthorityRetrievedContent || (it.Authority == domain.AuthorityUser && !capable) {
				t.Fatalf("%s span produced a semantic item", it.Authority)
			}
		}
	})
}
