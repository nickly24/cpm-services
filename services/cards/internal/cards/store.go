package cards

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Server) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	if e = fn(t); e != nil {
		return e
	}
	return t.Commit()
}
func theme(ctx context.Context, q querier, org, id int64, scope Scope, lock bool) (Theme, error) {
	var t Theme
	query := "SELECT id,direction_id,name,revision FROM cards_themes_v1 WHERE organization_id=? AND id=?"
	if lock {
		query += " FOR UPDATE"
	}
	e := q.QueryRowContext(ctx, query, org, id).Scan(&t.ID, &t.DirectionID, &t.Name, &t.Revision)
	if e == nil && !scope.Allows(t.DirectionID) {
		e = missing
	}
	return t, e
}
func manual(ctx context.Context, q querier, org, id int64) ([]Card, error) {
	rows, e := q.QueryContext(ctx, "SELECT id,question,answer,sort_order,revision FROM cards_items_v1 WHERE organization_id=? AND theme_id=? ORDER BY sort_order,id", org, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		var c Card
		if e = rows.Scan(&c.ID, &c.Question, &c.Answer, &c.Order, &c.Revision); e != nil {
			return nil, e
		}
		c.Ref = fmt.Sprintf("card:%d", c.ID)
		c.Fingerprint = fingerprint(c.Question, c.Answer)
		c.Status = "unlearned"
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Server) themeSave(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	var in ThemeDTO
	if e := decode(r, &in); e != nil {
		return nil, e
	}
	if !validText(in.Name, 200) || !scope.Allows(in.DirectionID) || len(in.Questions) > 1000 {
		return nil, bad("invalid_theme")
	}
	if r.Method == "PUT" && len(in.Questions) > 0 {
		return nil, bad("questions_only_on_creation")
	}
	for _, c := range in.Questions {
		if e := validateCard(c); e != nil {
			return nil, e
		}
	}
	var id int64
	var e error
	if r.Method == "PUT" {
		id, e = number(r.PathValue("theme"))
		if e != nil {
			return nil, e
		}
	}
	if r.Method == "POST" {
		return s.command(r, a, scope, "theme:create", in, func(tx *sql.Tx) (any, int64, error) {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO cards_themes_v1(organization_id,direction_id,name) VALUES(?,?,?)", a.OrganizationID, in.DirectionID, strings.TrimSpace(in.Name))
			if e != nil {
				return nil, 0, e
			}
			id, e := res.LastInsertId()
			if e != nil {
				return nil, 0, e
			}
			for i, c := range in.Questions {
				order := int64(i + 1)
				if c.Order != nil {
					order = *c.Order
				}
				if _, e = tx.ExecContext(r.Context(), "INSERT INTO cards_items_v1(organization_id,theme_id,question,answer,sort_order) VALUES(?,?,?,?,?)", a.OrganizationID, id, strings.TrimSpace(c.Question), strings.TrimSpace(c.Answer), order); e != nil {
					return nil, 0, e
				}
			}
			return Theme{ID: id, DirectionID: in.DirectionID, Name: strings.TrimSpace(in.Name), Revision: 1}, in.DirectionID, nil
		})
	}
	return s.command(r, a, scope, fmt.Sprintf("theme:update:%d", id), in, func(tx *sql.Tx) (any, int64, error) {
		t, e := theme(r.Context(), tx, a.OrganizationID, id, scope, true)
		if e != nil {
			return nil, 0, e
		}
		if t.Revision != in.ExpectedRevision {
			return nil, 0, conflict
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE cards_themes_v1 SET name=?,direction_id=?,revision=revision+1 WHERE id=? AND organization_id=?", strings.TrimSpace(in.Name), in.DirectionID, id, a.OrganizationID)
		if e != nil {
			return nil, 0, e
		}
		t.Name = strings.TrimSpace(in.Name)
		t.DirectionID = in.DirectionID
		t.Revision++
		return t, t.DirectionID, nil
	})
}
func (s *Server) themeDelete(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	id, e := number(r.PathValue("theme"))
	if e != nil {
		return nil, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, e
	}
	return s.command(r, a, scope, fmt.Sprintf("theme:delete:%d", id), in, func(tx *sql.Tx) (any, int64, error) {
		t, e := theme(r.Context(), tx, a.OrganizationID, id, scope, true)
		if e != nil {
			return nil, 0, e
		}
		if t.Revision != in.ExpectedRevision {
			return nil, 0, conflict
		}
		for _, table := range []string{"cards_progress_v1", "cards_settings_v1"} {
			if _, e = tx.ExecContext(r.Context(), "DELETE FROM "+table+" WHERE organization_id=? AND section_kind='manual' AND section_ref=?", a.OrganizationID, fmt.Sprint(id)); e != nil {
				return nil, 0, e
			}
		}
		_, e = tx.ExecContext(r.Context(), "DELETE FROM cards_themes_v1 WHERE organization_id=? AND id=?", a.OrganizationID, id)
		return map[string]any{"id": id}, t.DirectionID, e
	})
}
func validateCard(c CardDTO) error {
	if !validText(c.Question, 4000) || !validText(c.Answer, 16000) || c.Order != nil && (*c.Order < 0 || *c.Order > 1000000000) {
		return bad("invalid_card")
	}
	return nil
}
func (s *Server) adminCards(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(false) {
		return nil, denied
	}
	id, e := number(r.PathValue("theme"))
	if e != nil {
		return nil, e
	}
	if _, e = theme(r.Context(), s.DB, a.OrganizationID, id, scope, false); e != nil {
		return nil, e
	}
	return manual(r.Context(), s.DB, a.OrganizationID, id)
}
func (s *Server) cardSave(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	tid, e := number(r.PathValue("theme"))
	if e != nil {
		return nil, e
	}
	var in CardDTO
	if e = decode(r, &in); e != nil {
		return nil, e
	}
	if e = validateCard(in); e != nil {
		return nil, e
	}
	var id int64
	if r.Method == "PUT" {
		id, e = number(r.PathValue("card"))
		if e != nil {
			return nil, e
		}
	}
	if r.Method == "POST" {
		return s.command(r, a, scope, fmt.Sprintf("card:create:%d", tid), in, func(tx *sql.Tx) (any, int64, error) {
			t, e := theme(r.Context(), tx, a.OrganizationID, tid, scope, true)
			if e != nil {
				return nil, 0, e
			}
			var order int64
			if e = tx.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(sort_order),0)+1 FROM cards_items_v1 WHERE organization_id=? AND theme_id=?", a.OrganizationID, tid).Scan(&order); e != nil {
				return nil, 0, e
			}
			if in.Order != nil {
				order = *in.Order
			}
			res, e := tx.ExecContext(r.Context(), "INSERT INTO cards_items_v1(organization_id,theme_id,question,answer,sort_order) VALUES(?,?,?,?,?)", a.OrganizationID, tid, strings.TrimSpace(in.Question), strings.TrimSpace(in.Answer), order)
			if e != nil {
				return nil, 0, e
			}
			id, e := res.LastInsertId()
			if e != nil {
				return nil, 0, e
			}
			c := Card{ID: id, Ref: fmt.Sprintf("card:%d", id), Question: strings.TrimSpace(in.Question), Answer: strings.TrimSpace(in.Answer), Order: order, Revision: 1, Status: "unlearned"}
			c.Fingerprint = fingerprint(c.Question, c.Answer)
			return c, t.DirectionID, nil
		})
	}
	return s.command(r, a, scope, fmt.Sprintf("card:update:%d:%d", tid, id), in, func(tx *sql.Tx) (any, int64, error) {
		t, e := theme(r.Context(), tx, a.OrganizationID, tid, scope, true)
		if e != nil {
			return nil, 0, e
		}
		var order, rev int64
		if e = tx.QueryRowContext(r.Context(), "SELECT sort_order,revision FROM cards_items_v1 WHERE organization_id=? AND theme_id=? AND id=? FOR UPDATE", a.OrganizationID, tid, id).Scan(&order, &rev); e != nil {
			return nil, 0, e
		}
		if rev != in.ExpectedRevision {
			return nil, 0, conflict
		}
		if in.Order != nil {
			order = *in.Order
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE cards_items_v1 SET question=?,answer=?,sort_order=?,revision=revision+1 WHERE organization_id=? AND theme_id=? AND id=?", strings.TrimSpace(in.Question), strings.TrimSpace(in.Answer), order, a.OrganizationID, tid, id)
		if e != nil {
			return nil, 0, e
		}
		c := Card{ID: id, Ref: fmt.Sprintf("card:%d", id), Question: strings.TrimSpace(in.Question), Answer: strings.TrimSpace(in.Answer), Order: order, Revision: rev + 1, Status: "unlearned"}
		c.Fingerprint = fingerprint(c.Question, c.Answer)
		return c, t.DirectionID, nil
	})
}
func (s *Server) cardDelete(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	tid, e := number(r.PathValue("theme"))
	if e != nil {
		return nil, e
	}
	id, e := number(r.PathValue("card"))
	if e != nil {
		return nil, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, e
	}
	return s.command(r, a, scope, fmt.Sprintf("card:delete:%d:%d", tid, id), in, func(tx *sql.Tx) (any, int64, error) {
		t, e := theme(r.Context(), tx, a.OrganizationID, tid, scope, true)
		if e != nil {
			return nil, 0, e
		}
		var rev int64
		if e := tx.QueryRowContext(r.Context(), "SELECT revision FROM cards_items_v1 WHERE organization_id=? AND theme_id=? AND id=? FOR UPDATE", a.OrganizationID, tid, id).Scan(&rev); e != nil {
			return nil, 0, e
		}
		if rev != in.ExpectedRevision {
			return nil, 0, conflict
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM cards_progress_v1 WHERE organization_id=? AND card_ref=?", a.OrganizationID, fmt.Sprintf("card:%d", id)); e != nil {
			return nil, 0, e
		}
		_, e = tx.ExecContext(r.Context(), "DELETE FROM cards_items_v1 WHERE organization_id=? AND theme_id=? AND id=?", a.OrganizationID, tid, id)
		return map[string]any{"id": id}, t.DirectionID, e
	})
}
