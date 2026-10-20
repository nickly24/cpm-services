package attendance

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
)

const maxCellBatch = 100

type CellEdit struct {
	DayID     int64  `json:"class_day_id"`
	AccountID int64  `json:"account_id"`
	TypeID    *int64 `json:"attendance_type_id"` // explicit 0 means erase
}
type CellBatch struct {
	Cells []CellEdit `json:"cells"`
}
type CellBatchResult struct {
	Cells []Entry `json:"cells"`
}
type cellTargetsPeer interface {
	CellTargets(context.Context, Identity, int64, []int64, string) (map[int64]Member, error)
}
type cellTargetsKey struct{}
type cellTargets struct {
	organization, division int64
	members                map[int64]Member
}

func cachedCellTarget(ctx context.Context, a Identity, d, u int64) (Member, bool) {
	v, ok := ctx.Value(cellTargetsKey{}).(cellTargets)
	if !ok || v.organization != a.User.OrganizationID || v.division != d {
		return Member{}, false
	}
	m, ok := v.members[u]
	return m, ok
}
func (s *Server) combinedCells(kind string) bool {
	_, ok := s.Peers.(cellTargetsPeer)
	return ok && (kind == "mark" || kind == "unmark" || kind == "cells")
}
func cellEdits(kind string, ids map[string]int64, raw json.RawMessage) ([]CellEdit, error) {
	switch kind {
	case "mark":
		var m Mark
		if decodeJSON(raw, &m) != nil || m.AccountID < 1 || m.TypeID < 1 {
			return nil, bad("invalid_mark")
		}
		return []CellEdit{{ids["day"], m.AccountID, &m.TypeID}}, nil
	case "unmark":
		if decodeJSON(raw, &struct{}{}) != nil {
			return nil, bad("invalid_request")
		}
		zero := int64(0)
		return []CellEdit{{ids["day"], ids["account"], &zero}}, nil
	case "cells":
		var b CellBatch
		if decodeJSON(raw, &b) != nil || len(b.Cells) < 1 || len(b.Cells) > maxCellBatch {
			return nil, bad("invalid_cell_batch")
		}
		seen := map[string]bool{}
		for _, c := range b.Cells {
			k := key(c.DayID, c.AccountID)
			if c.DayID < 1 || c.AccountID < 1 || c.TypeID == nil || *c.TypeID < 0 || seen[k] {
				return nil, bad("invalid_cell_batch")
			}
			seen[k] = true
		}
		return b.Cells, nil
	}
	return nil, bad("invalid_cell_batch")
}
func validateCellEdits(st *State, d int64, cells []CellEdit) error {
	for _, c := range cells {
		day, ok := st.Days[c.DayID]
		if !ok || day.DivisionID != d {
			return missing
		}
		old, exists := st.Entries[key(c.DayID, c.AccountID)]
		if exists && old.DivisionID != d {
			return missing
		}
		if old.ZapID != nil && !old.Deleted {
			return conflict("attendance_linked_to_zap")
		}
		if *c.TypeID == 0 {
			if !exists || old.Deleted {
				return missing
			}
		} else {
			t, ok := st.Types[*c.TypeID]
			if !ok || t.DivisionID != d || !t.Active {
				return bad("invalid_attendance_type")
			}
		}
	}
	return nil
}
func (s *Server) prepareCellTargets(ctx context.Context, a Identity, d int64, cells []CellEdit, op string) (context.Context, error) {
	seen := map[int64]bool{}
	ids := []int64{}
	for _, c := range cells {
		// Full attendance editors could always erase historical marks without
		// requiring the student account to remain active. Proctors still need
		// live membership/group checks; every new mark needs a permanent guard.
		if *c.TypeID == 0 && a.can(d, "attendance", true) {
			continue
		}
		if !seen[c.AccountID] {
			seen[c.AccountID] = true
			ids = append(ids, c.AccountID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	members := map[int64]Member{}
	if peer, ok := s.Peers.(cellTargetsPeer); ok {
		var err error
		members, err = peer.CellTargets(ctx, a, d, ids, op)
		if err != nil {
			return ctx, err
		}
	} else {
		if len(ids) == 0 {
			if _, err := s.Peers.Division(ctx, a.User.OrganizationID, d); err != nil {
				return ctx, err
			}
		}
		for _, id := range ids {
			m, err := s.target(ctx, a, d, id, false)
			if err != nil {
				return ctx, err
			}
			if err = s.Peers.Guard(ctx, a.User.OrganizationID, d, id, op); err != nil {
				return ctx, err
			}
			members[id] = m
		}
	}
	return context.WithValue(ctx, cellTargetsKey{}, cellTargets{a.User.OrganizationID, d, members}), nil
}
func (s *Server) writeCellCommand(r *http.Request, st *State, a Identity, d int64, ids map[string]int64, kind string, raw json.RawMessage, op string) (any, error) {
	if kind != "cells" && !s.combinedCells(kind) {
		return s.write(r, st, a, d, ids, kind, raw, op)
	}
	cells, err := cellEdits(kind, ids, raw)
	if err != nil {
		return nil, err
	}
	if err = validateCellEdits(st, d, cells); err != nil {
		return nil, err
	}
	ctx, err := s.prepareCellTargets(r.Context(), a, d, cells, op)
	if err != nil {
		return nil, err
	}
	if kind != "cells" {
		return s.write(r.WithContext(ctx), st, a, d, ids, kind, raw, op)
	}
	out := CellBatchResult{Cells: []Entry{}}
	for _, c := range cells {
		if *c.TypeID == 0 {
			_, err = s.deleteMark(ctx, st, a, d, c.DayID, c.AccountID)
		} else {
			_, err = s.setMark(ctx, st, a, d, c.DayID, Mark{c.AccountID, *c.TypeID}, op, false)
		}
		if err != nil {
			return nil, err
		}
		out.Cells = append(out.Cells, st.Entries[key(c.DayID, c.AccountID)])
	}
	return out, nil
}

// Batch evidence is canonical per-cell state, including tombstones. Never infer
// targets from today's entry, which may have been corrected since this receipt.
func batchReceiptEntries(original *State, result Result, d int64) ([]Entry, error) {
	raw, err := json.Marshal(result.Value)
	var batch CellBatchResult
	if err != nil || decodeJSON(raw, &batch) != nil || len(batch.Cells) < 1 || len(batch.Cells) > maxCellBatch {
		return nil, unavailable
	}
	seen := map[string]bool{}
	for _, entry := range batch.Cells {
		k := key(entry.DayID, entry.AccountID)
		old, ok := original.Entries[k]
		if entry.ID < 1 || entry.DayID < 1 || entry.AccountID < 1 || entry.DivisionID != d || seen[k] || !ok {
			return nil, unavailable
		}
		seen[k] = true
		x, _ := json.Marshal(old)
		y, _ := json.Marshal(entry)
		if string(x) != string(y) {
			return nil, unavailable
		}
	}
	return batch.Cells, nil
}
func (s *Server) replayCellAuthorization(r *http.Request, a Identity, d int64, ids map[string]int64, kind string) func(*State, Result) error {
	if kind != "cells" && !s.combinedCells(kind) {
		return s.replayAuthorization(r, a, d, ids, kind)
	}
	return func(original *State, result Result) error {
		if kind != "cells" {
			if err := validateJournalReceipt(receiptEvidence{Result: result, Route: commandRoute{Kind: kind, Division: d, IDs: ids}, Original: original}); err != nil {
				return err
			}
		}
		entries := []Entry{}
		if kind == "cells" {
			var err error
			entries, err = batchReceiptEntries(original, result, d)
			if err != nil {
				return err
			}
		} else {
			u := ids["account"]
			if kind == "mark" {
				raw, _ := json.Marshal(result.Value)
				var entry Entry
				if decodeJSON(raw, &entry) != nil || entry.DivisionID != d || entry.DayID != ids["day"] || entry.AccountID < 1 {
					return unavailable
				}
				u = entry.AccountID
			}
			entry, ok := original.Entries[key(ids["day"], u)]
			if !ok || entry.DivisionID != d {
				return unavailable
			}
			entries = append(entries, entry)
		}
		cells := []CellEdit{}
		for _, e := range entries {
			zero := int64(0)
			cells = append(cells, CellEdit{e.DayID, e.AccountID, &zero})
		}
		_, err := s.prepareCellTargets(r.Context(), a, d, cells, "attendance-receipt-recheck")
		if err != nil {
			return err
		}
		if a.User.Role == "proctor" {
			grant, ok := a.division(d)
			if !ok {
				return denied
			}
			for _, e := range entries {
				if !same(grant.GroupID, e.GroupID) {
					return denied
				}
			}
		}
		return nil
	}
}
