package sqlite

import (
	"context"
	"database/sql"
	"path"
	"strings"
)

// Migration 0011's Go step, frozen (F5). It indexes the source locator key
// of every item stored before 0011 under locator rule v1. The v1 transform
// is copied here verbatim rather than called from domain, so this step's
// effect never changes with the binary; TestCommittedStepsUnchanged pins
// this file.

const (
	locatorRuleV1        = "reference-locator/v1"
	maxLocatorKeyBytesV1 = 4096
	sourcePathV1, urlV1  = "path", "url"
)

func backfillItemSourcesV1(ctx context.Context, conn *sql.Conn) error {
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
		if key, ok := locatorKeyV1(kind.String, locator.String); ok {
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
		if _, err := conn.ExecContext(ctx, "INSERT INTO item_sources(session_id,rule_version,locator_key,item_id) VALUES(?,?,?,?)", f.session, locatorRuleV1, f.key, f.id); err != nil {
			return err
		}
	}
	return nil
}

// locatorKeyV1 is locator rule v1: "url:" + an exact URL, or "path:" + a
// lexically cleaned relative path that does not escape its base; anything
// else is not a locator.
func locatorKeyV1(kind, locator string) (string, bool) {
	if locator == "" || len(locator) > maxLocatorKeyBytesV1-5 {
		return "", false
	}
	for i := range len(locator) {
		if c := locator[i]; c <= ' ' || c == 0x7f || c == '\\' {
			return "", false
		}
	}
	switch kind {
	case urlV1:
		if !isURLV1(locator) {
			return "", false
		}
		return "url:" + locator, true
	case sourcePathV1:
		if strings.HasPrefix(locator, "/") || isURLV1(locator) {
			return "", false
		}
		clean := path.Clean(locator)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return "", false
		}
		return "path:" + clean, true
	}
	return "", false
}

func isURLV1(s string) bool {
	scheme, _, ok := strings.Cut(s, "://")
	if !ok || scheme == "" {
		return false
	}
	for i := range len(scheme) {
		c := scheme[i]
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !alpha && (i == 0 || !(c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}
