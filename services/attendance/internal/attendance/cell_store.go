package attendance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// A cell command needs one day, one type and one previous entry. The organization
// revision/lock and full immutable audit stay unchanged; unrelated state never
// crosses the database connection for this command.
type cellProjection struct {
	day, account, typ int64
	entries           int
	existed           bool
}

func (p *cellProjection) entryKey() string         { return key(p.day, p.account) }
func cellPath(collection string, id string) string { return "$." + collection + ".\"" + id + "\"" }
func projectedCell(kind string, ids map[string]int64, body json.RawMessage) *cellProjection {
	if kind == "unmark" {
		return &cellProjection{day: ids["day"], account: ids["account"]}
	}
	if kind != "mark" {
		return nil
	}
	var m Mark
	if decodeJSON(body, &m) != nil || m.AccountID < 1 || m.TypeID < 1 {
		return nil
	}
	return &cellProjection{day: ids["day"], account: m.AccountID, typ: m.TypeID}
}
func (p *cellProjection) read(ctx context.Context, tx *sql.Tx, organization int64) ([]byte, int64, error) {
	var data []byte
	var revision int64
	// JSON_OBJECT keeps the existing State shape so all owner validation and
	// canonical Entry construction are shared with the ordinary write path.
	err := tx.QueryRowContext(ctx, `SELECT revision,JSON_OBJECT(
 'next_id',JSON_EXTRACT(document,'$.next_id'),
 'days',JSON_OBJECT(?,JSON_EXTRACT(document,?)),
 'types',JSON_OBJECT(?,JSON_EXTRACT(document,?)),
 'entries',JSON_OBJECT(?,JSON_EXTRACT(document,?))),
 JSON_LENGTH(document,'$.entries') FROM attendance_state_v1 WHERE organization_id=? FOR UPDATE`,
		fmt.Sprint(p.day), cellPath("days", fmt.Sprint(p.day)), fmt.Sprint(p.typ), cellPath("types", fmt.Sprint(p.typ)), key(p.day, p.account), cellPath("entries", key(p.day, p.account)), organization).Scan(&revision, &data, &p.entries)
	return data, revision, err
}
func (p *cellProjection) persist(ctx context.Context, tx *sql.Tx, organization, revision int64, st *State) error {
	e, exists := st.Entries[key(p.day, p.account)]
	if !exists || e.ID < 1 || e.DayID != p.day || e.AccountID != p.account {
		return unavailable
	}
	entry, err := json.Marshal(e)
	if err != nil {
		return unavailable
	}
	day, ok := st.Days[p.day]
	if !ok {
		return unavailable
	}
	d, err := json.Marshal(day)
	if err != nil {
		return unavailable
	}
	_, err = tx.ExecContext(ctx, `UPDATE attendance_state_v1 SET revision=?,document=JSON_SET(document,
 '$.next_id',?, ?,CAST(? AS JSON), ?,CAST(? AS JSON)) WHERE organization_id=?`, revision, st.NextID, cellPath("entries", key(p.day, p.account)), string(entry), cellPath("days", fmt.Sprint(p.day)), string(d), organization)
	if err != nil {
		return dbError(err)
	}
	var bytes int
	if err = tx.QueryRowContext(ctx, "SELECT OCTET_LENGTH(document) FROM attendance_state_v1 WHERE organization_id=?", organization).Scan(&bytes); err != nil {
		return dbError(err)
	}
	if bytes > 32<<20 {
		return bad("dev_capacity_exceeded")
	}
	return nil
}
