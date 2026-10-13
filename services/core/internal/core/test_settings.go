package core

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"net/http"
)

// The revision and value must be frozen in the test edit event. A later toggle
// changes future edits only; it does not alter an edit already accepted.
type OrganizationTestSettings struct {
	OrganizationID          int64 `json:"organization_id"`
	RecalculateOnTestChange bool  `json:"recalculate_on_test_change"`
	Revision                int64 `json:"revision"`
}

type OrganizationTestSettingsDTO struct {
	ExpectedRevision        *int64 `json:"expected_revision"`
	RecalculateOnTestChange *bool  `json:"recalculate_on_test_change"`
}

func readOrganizationTestSettings(ctx context.Context, db DBTX, organization int64) (OrganizationTestSettings, error) {
	var out OrganizationTestSettings
	err := db.QueryRowContext(ctx, `SELECT o.id,COALESCE(s.recalculate_on_test_change,1),COALESCE(s.revision,0)
		FROM organizations o LEFT JOIN core_organization_test_settings s ON s.organization_id=o.id
		WHERE o.id=? AND o.is_active=1`, organization).Scan(&out.OrganizationID, &out.RecalculateOnTestChange, &out.Revision)
	return out, err
}

func (s *Server) organizationTestSettings(r *http.Request, a Actor, _ int64) (any, int, error) {
	out, err := readOrganizationTestSettings(r.Context(), s.DB, a.OrganizationID)
	return out, 200, err
}

func (s *Server) organizationTestSettingsUpdate(r *http.Request, a Actor, _ int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in OrganizationTestSettingsDTO
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || in.RecalculateOnTestChange == nil {
		return nil, 0, bad("invalid_test_settings")
	}
	var out OrganizationTestSettings
	err := s.transact(r.Context(), func(tx *sql.Tx) error {
		// The organization row also serializes the first update when no setting
		// row exists yet, so the default revision zero has normal CAS semantics.
		var organization int64
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM organizations WHERE id=? AND is_active=1 FOR UPDATE", a.OrganizationID).Scan(&organization); err != nil {
			return err
		}
		var err error
		out, err = readOrganizationTestSettings(r.Context(), tx, organization)
		if err != nil {
			return err
		}
		if out.Revision != *in.ExpectedRevision {
			return &APIError{409, "revision_conflict"}
		}
		if out.RecalculateOnTestChange == *in.RecalculateOnTestChange {
			return nil
		}
		out.RecalculateOnTestChange = *in.RecalculateOnTestChange
		out.Revision++
		if out.Revision == 1 {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO core_organization_test_settings(organization_id,recalculate_on_test_change,revision,updated_by) VALUES(?,?,?,?)", organization, out.RecalculateOnTestChange, out.Revision, a.AccountID)
		} else {
			_, err = tx.ExecContext(r.Context(), "UPDATE core_organization_test_settings SET recalculate_on_test_change=?,revision=?,updated_by=?,updated_at=CURRENT_TIMESTAMP(6) WHERE organization_id=? AND revision=?", out.RecalculateOnTestChange, out.Revision, a.AccountID, organization, *in.ExpectedRevision)
		}
		if err != nil {
			return err
		}
		action := "organization.test-recalculation.disabled"
		if out.RecalculateOnTestChange {
			action = "organization.test-recalculation.enabled"
		}
		return audit(r.Context(), tx, a, nil, action, organization)
	})
	return out, 200, err
}

func (s *Server) internalOrganizationTestSettings(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	organization, err := id(r, "organization")
	if err != nil {
		fail(w, err)
		return
	}
	out, err := readOrganizationTestSettings(r.Context(), s.DB, organization)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": out})
}
