package ingest

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestRetryAfterLimitsChange_F3 is SPEC-1.7/DUR-1.2 (F3): an exact retry of
// a known EventID replays its stored receipt before any limit or policy
// check, so tightening trusted configuration never turns a committed
// event's retry into a failure; new events still meet the new limits.
func TestRetryAfterLimitsChange_F3(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("lim", "## Pinned\n- one\n- two\n"+strings.Repeat("x", 100), true)
		first := f.mustIngest(user, e)
		seq := f.lastSeq()
		for name, l := range map[string]domain.Limits{
			"MaxSpanBytes":    {MaxSpanBytes: 8},
			"MaxEventBytes":   {MaxEventBytes: 8},
			"MaxEventItems":   {MaxEventItems: 1},
			"MaxItemsPerSpan": {MaxItemsPerSpan: 1},
		} {
			f.in.Limits = l
			again, err := f.ingest(user, e)
			if err != nil || !reflect.DeepEqual(again, first) || f.lastSeq() != seq {
				t.Errorf("%s: retry = %v (equal %v)", name, err, reflect.DeepEqual(again, first))
			}
			fresh := e
			fresh.EventID = "new-" + name
			if _, err := f.ingest(user, fresh); !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("%s: a new event ignored the limit: %v", name, err)
			}
		}
	})
}
