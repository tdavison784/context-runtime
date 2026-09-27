package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"path"
	"sort"
	"strings"
)

// reconcileLiveProofPathsV1 is migration 0045's frozen step (DUR-3.1). For
// every live proof (the current proof of a current obligation version) it
// rebuilds lookup_live_dependency without FIXED_CONTENT dependencies, files
// each CURRENT_PATH dependency in lookup_live_proof_path under every
// ancestor key of its path and each WORKSPACE dependency under 'ws', and
// counts live non-FIXED dependency rows per resource. The resource-locator
// key (resource-locator/v1) and the canonical encoder are copied here
// verbatim, as are the column names and written values; a changed rule is
// a new migration.
func reconcileLiveProofPathsV1(ctx context.Context, c *sql.Conn) error {
	rows, err := c.QueryContext(ctx, `SELECT o.session_id, o.f_current_proof_id, p.f_seq
FROM rec_obligation AS o JOIN rec_proof AS p ON p.session_id = o.session_id AND p.id = o.f_current_proof_id AND p.subkey = 0
WHERE o.f_current = 1 AND COALESCE(o.f_current_proof_id, '') != ''
ORDER BY o.session_id, p.f_seq, o.f_current_proof_id`)
	if err != nil {
		return err
	}
	type liveProof struct {
		session, proof string
		seq            int64
	}
	var proofs []liveProof
	for rows.Next() {
		var l liveProof
		if err := rows.Scan(&l.session, &l.proof, &l.seq); err != nil {
			rows.Close()
			return err
		}
		proofs = append(proofs, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "DELETE FROM lookup_live_dependency"); err != nil {
		return err
	}
	type counterKey struct{ session, resource string }
	counts := map[counterKey]int64{}
	for _, l := range proofs {
		deps, err := c.QueryContext(ctx, `SELECT COALESCE(f_resource_id, ''), COALESCE(f_kind, ''), f_locator_present,
  COALESCE(f_locator_resource_id, ''), COALESCE(f_locator_base_dir, ''), COALESCE(f_locator_path, '')
FROM rec_proof_dependency WHERE session_id = ? AND f_proof_id = ? ORDER BY id`, l.session, l.proof)
		if err != nil {
			return err
		}
		liveKeys, paths, resources := map[[2]string]bool{}, map[[2]string]bool{}, map[string]int64{}
		for deps.Next() {
			var resource, kind, locResource, base, name string
			var present int64
			if err := deps.Scan(&resource, &kind, &present, &locResource, &base, &name); err != nil {
				deps.Close()
				return err
			}
			if kind == "FIXED_CONTENT" {
				continue
			}
			resources[resource]++
			key := ""
			if present == 1 {
				key = liveLocatorKeyV1(locResource, base, name)
			}
			liveKeys[[2]string{resource, key}] = true
			switch kind {
			case "WORKSPACE":
				paths[[2]string{resource, "ws"}] = true
			case "CURRENT_PATH":
				full := path.Join(base, name)
				for i := range full {
					if full[i] == '/' {
						paths[[2]string{resource, "path:" + hex.EncodeToString([]byte(full[:i]))}] = true
					}
				}
				paths[[2]string{resource, "path:" + hex.EncodeToString([]byte(full))}] = true
			}
		}
		if err := deps.Err(); err != nil {
			deps.Close()
			return err
		}
		if err := deps.Close(); err != nil {
			return err
		}
		for _, k := range sortedPairsV1(liveKeys) {
			if _, err := c.ExecContext(ctx, "INSERT INTO lookup_live_dependency(session_id,resource_id,path_key,seq,proof_id) VALUES(?,?,?,?,?)",
				l.session, k[0], k[1], l.seq, l.proof); err != nil {
				return err
			}
		}
		for _, k := range sortedPairsV1(paths) {
			if _, err := c.ExecContext(ctx, "INSERT INTO lookup_live_proof_path(session_id,resource_id,key,seq,proof_id) VALUES(?,?,?,?,?)",
				l.session, k[0], k[1], l.seq, l.proof); err != nil {
				return err
			}
		}
		for r, rows := range resources {
			counts[counterKey{l.session, r}] += rows
		}
	}
	keys := make([]counterKey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].session < keys[j].session || keys[i].session == keys[j].session && keys[i].resource < keys[j].resource
	})
	for _, k := range keys {
		if _, err := c.ExecContext(ctx, "INSERT INTO lookup_live_dependents(session_id,resource_id,dependents) VALUES(?,?,?)", k.session, k.resource, counts[k]); err != nil {
			return err
		}
	}
	return nil
}

// liveLocatorKeyV1 is domain.ResourceLocator.Key (resource-locator/v1) of
// a stored, already canonical locator, frozen.
func liveLocatorKeyV1(resource, base, name string) string {
	var buf []byte
	for _, s := range []string{"context-runtime/resource-locator/v1", resource, base, name} {
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		buf = append(buf, s...)
	}
	sum := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedPairsV1(set map[[2]string]bool) [][2]string {
	out := make([][2]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i][0]+"\x00"+out[i][1], out[j][0]+"\x00"+out[j][1]) < 0
	})
	return out
}
