package sqlite

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestReceiptRowCoversReceipt fails when domain.IngestReceipt gains a field
// receiptRow does not store, so a receipt can never lose data silently.
func TestReceiptRowCoversReceipt(t *testing.T) {
	nested := map[string]bool{"Items": true, "Diagnostics": true, "Lifecycle": true}
	row := reflect.TypeFor[receiptRow]()
	rt := reflect.TypeFor[domain.IngestReceipt]()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if nested[f.Name] {
			continue
		}
		g, ok := row.FieldByName(f.Name)
		if !ok || g.Type != f.Type {
			t.Errorf("receiptRow does not store IngestReceipt.%s (%s)", f.Name, f.Type)
		}
	}
}
