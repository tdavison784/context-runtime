package graph

import "github.com/tdavison784/context-runtime/internal/store"

// Graph mutations are constituent semantic operations. A caller that ignores
// their error must not commit its earlier item/declaration or partial edge set.
func poisonGraphError(tx store.Tx, err *error) {
	if *err != nil {
		tx.Poison(*err)
	}
}
