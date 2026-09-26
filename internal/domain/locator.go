package domain

import (
	"path"
	"strings"
)

// LocatorRuleVersion names locator identity rule v1 (M5, R2, R19). It is the
// rule internal/ingest uses for References and source matching; it lives in
// domain so the store can index item sources under exactly the same keys.
const LocatorRuleVersion = "reference-locator/v1"

// LocatorKey returns the rule-v1 key of a locator of the given kind, or
// ok=false when it is not a linkable locator. The key is purely lexical;
// nothing is fetched, opened, or resolved:
//
//   - a URL (scheme "://" rest, scheme = ALPHA *(ALPHA / DIGIT / "+" / "-"
//     / ".")) keys as "url:" + its exact bytes;
//   - anything else is a path: it must be relative and must not escape its
//     base after lexical cleaning (path.Clean), and keys as "path:" + the
//     cleaned path, so "./docs//a.md" and "docs/a.md" match;
//   - empty text, whitespace or control bytes, backslashes, absolute paths,
//     escaping paths, and other source kinds are not locators.
//
// There is no repository or namespace component at all in v1 (SPEC-1.13:
// corrected — not a per-session default): a locator is matched on its
// lexical bytes alone within the session, so the same relative path in two
// conceptually different repositories is one target. Multi-repository
// identity is deferred (R19).
func LocatorKey(kind SourceKind, locator string) (string, bool) {
	if locator == "" || len(locator) > MaxLocatorKeyBytes-5 {
		return "", false
	}
	for i := range len(locator) {
		if c := locator[i]; c <= ' ' || c == 0x7f || c == '\\' {
			return "", false
		}
	}
	switch kind {
	case SourceURL:
		if !isLocatorURL(locator) {
			return "", false
		}
		return "url:" + locator, true
	case SourcePath:
		if strings.HasPrefix(locator, "/") || isLocatorURL(locator) {
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

func isLocatorURL(s string) bool {
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
