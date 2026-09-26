package sqlite

import (
	"context"
	"database/sql"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// migrationSteps are Go steps that run after a migration's SQL, inside the
// same transaction, for data changes SQL cannot express. A step belongs to
// its migration forever: like the SQL file, it is never edited once
// committed (a fix is a new migration).
var migrationSteps = map[int]func(context.Context, *sql.Conn) error{
	11: backfillItemSources,
}

// backfillItemSources indexes the source locator key (domain.LocatorKey,
// rule v1) of every item stored before migration 0011. Items whose source
// is not a linkable locator stay unindexed, as InsertItem leaves them.
func backfillItemSources(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "SELECT session_id, id, f_source_kind, f_source_locator FROM rec_item WHERE subkey=0 AND f_source_present=1")
	if err != nil {
		return err
	}
	type source struct{ session, id, key string }
	var found []source
	for rows.Next() {
		var session, id string
		var kind, locator sql.NullString
		if err := rows.Scan(&session, &id, &kind, &locator); err != nil {
			rows.Close()
			return err
		}
		if key, ok := domain.LocatorKey(domain.SourceKind(kind.String), locator.String); ok {
			found = append(found, source{session, id, key})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, f := range found {
		if _, err := conn.ExecContext(ctx, "INSERT INTO item_sources(session_id,rule_version,locator_key,item_id) VALUES(?,?,?,?)", f.session, domain.LocatorRuleVersion, f.key, f.id); err != nil {
			return err
		}
	}
	return nil
}
