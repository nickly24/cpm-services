package attendance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Fetch and update only the bounded cells, days and types needed by a command.
// The organization lock/CAS, receipt and full immutable audit are unchanged.
type cellProjection struct {
	day, account, typ int64
	cells             []CellEdit
	entries, existing int
}

func cellPath(collection string, id string) string { return "$." + collection + ".\"" + id + "\"" }
func (p *cellProjection) edits() []CellEdit {
	if p.cells != nil {
		return p.cells
	}
	return []CellEdit{{p.day, p.account, &p.typ}}
}
func projectedCell(kind string, ids map[string]int64, body json.RawMessage) *cellProjection {
	if kind != "mark" && kind != "unmark" && kind != "cells" {
		return nil
	}
	cells, err := cellEdits(kind, ids, body)
	if err != nil {
		return nil
	}
	return &cellProjection{cells: cells}
}
func (p *cellProjection) read(ctx context.Context, tx *sql.Tx, organization int64) ([]byte, int64, error) {
	collections := map[string]map[string]bool{"days": {}, "types": {}, "entries": {}}
	for _, c := range p.edits() {
		collections["days"][fmt.Sprint(c.DayID)] = true
		collections["types"][fmt.Sprint(*c.TypeID)] = true
		collections["entries"][key(c.DayID, c.AccountID)] = true
	}
	query := `SELECT revision,JSON_OBJECT('next_id',JSON_EXTRACT(document,'$.next_id')`
	args := []any{}
	for _, collection := range []string{"days", "types", "entries"} {
		query += ",'" + collection + "',JSON_OBJECT("
		ids := []string{}
		for id := range collections[collection] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		parts := []string{}
		for _, id := range ids {
			parts = append(parts, "?,JSON_EXTRACT(document,?)")
			args = append(args, id, cellPath(collection, id))
		}
		query += strings.Join(parts, ",") + ")"
	}
	query += `),JSON_LENGTH(document,'$.entries') FROM attendance_state_v1 WHERE organization_id=? FOR UPDATE`
	args = append(args, organization)
	var data []byte
	var revision int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&revision, &data, &p.entries)
	return data, revision, err
}
func (p *cellProjection) persist(ctx context.Context, tx *sql.Tx, organization, revision int64, st *State) error {
	query := `UPDATE attendance_state_v1 SET revision=?,document=JSON_SET(document,'$.next_id',?`
	args := []any{revision, st.NextID}
	days := map[int64]bool{}
	for _, c := range p.edits() {
		e, ok := st.Entries[key(c.DayID, c.AccountID)]
		if !ok || e.ID < 1 || e.DayID != c.DayID || e.AccountID != c.AccountID {
			return unavailable
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return unavailable
		}
		query += ",?,CAST(? AS JSON)"
		args = append(args, cellPath("entries", key(c.DayID, c.AccountID)), string(raw))
		days[c.DayID] = true
	}
	ids := []int64{}
	for id := range days {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d, ok := st.Days[id]
		if !ok {
			return unavailable
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return unavailable
		}
		query += ",?,CAST(? AS JSON)"
		args = append(args, cellPath("days", fmt.Sprint(id)), string(raw))
	}
	query += ") WHERE organization_id=?"
	args = append(args, organization)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return dbError(err)
	}
	var bytes int
	if err := tx.QueryRowContext(ctx, "SELECT OCTET_LENGTH(document) FROM attendance_state_v1 WHERE organization_id=?", organization).Scan(&bytes); err != nil {
		return dbError(err)
	}
	if bytes > 32<<20 {
		return bad("dev_capacity_exceeded")
	}
	return nil
}
