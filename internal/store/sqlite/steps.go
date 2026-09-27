package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
)

// migrationStep is Go code that runs after a migration's SQL, inside the
// same transaction, for data changes SQL cannot express. A step belongs to
// its migration forever: its code is frozen in its own file (no calls into
// live domain rules), and its identity is part of the migration's stored
// checksum, so a database migrated by one step refuses a binary whose step
// differs (F5: DUR-1.8, SPEC-1.9). A fix is a new migration.
type migrationStep struct {
	id  string
	run func(context.Context, *sql.Conn) error
}

var migrationSteps = map[int]migrationStep{
	11: {id: "0011/item-sources/reference-locator-v1", run: backfillItemSourcesV1},
	26: {id: "0026/obligations/reconcile-matcher-satisfaction-v1", run: reconcileMatcherSatisfactionV1},
	34: {id: "0034/declarations/reconcile-legacy-creation-v1", run: reconcileLegacyCreationV1},
	45: {id: "0045/proofs/reconcile-live-proof-paths-v1", run: reconcileLiveProofPathsV1},
}

// migrationChecksum is the stored checksum of migration number: the SHA-256
// of its SQL, extended with its Go step's identity when it has one.
func migrationChecksum(sqlBytes []byte, number int) string {
	in := sqlBytes
	if step, ok := migrationSteps[number]; ok {
		in = append(append([]byte{}, sqlBytes...), "\n-- go-step: "+step.id...)
	}
	sum := sha256.Sum256(in)
	return hex.EncodeToString(sum[:])
}
