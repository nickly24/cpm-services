package exams

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type catalogRecord struct {
	Exam      *Exam
	Assigned  int
	Parts     int
	Questions int
}

// Catalogs must never hydrate the question bank or immutable attempt snapshots.
// Existing JSON storage still requires a scan inside MySQL, but wire/memory cost
// is proportional to compact metadata, independent of each exam's history size.
func catalogProjection() string {
	fields := []string{"id", "name", "organization_id", "owner_division_id", "direction_id", "exam_type", "revision", "content_revision", "in_bank", "origin_exam_id", "created_at"}
	pairs := []string{}
	for _, key := range fields {
		pairs = append(pairs, "'"+key+"',JSON_EXTRACT(document,'$."+key+"')")
	}
	return "JSON_OBJECT(" + strings.Join(pairs, ",") + ")"
}
func listCatalog(ctx context.Context, db *sql.DB, a Actor, p Params) ([]catalogRecord, error) {
	path := fmt.Sprintf("$.divisions.\"%d\"", p.DivisionID)
	// Identifiers come only from parsed integer identity/context fields.
	assigned := "0"
	if a.Role == "examinator" {
		assigned = fmt.Sprintf(`(SELECT COUNT(*) FROM JSON_TABLE(JSON_EXTRACT(document,'%s.assignments'),'$[*]' COLUMNS(members JSON PATH '$.members')) aa WHERE JSON_CONTAINS(JSON_EXTRACT(aa.members,'$[*].account_id'),CAST(%d AS JSON)))`, path, a.AccountID)
	}
	query := `SELECT ` + catalogProjection() + `,JSON_KEYS(JSON_EXTRACT(document,'$.divisions')),JSON_OBJECT('date',JSON_EXTRACT(document,'` + path + `.date'),'revision',JSON_EXTRACT(document,'` + path + `.revision'),'config',JSON_EXTRACT(document,'` + path + `.config'),'training_enabled',JSON_EXTRACT(document,'` + path + `.training_enabled')),COALESCE(JSON_LENGTH(JSON_EXTRACT(document,'$.parts')),0),COALESCE(JSON_LENGTH(JSON_EXTRACT(document,'$.questions')),0),` + assigned + ` FROM exams_catalog_v1 WHERE organization_id=?`
	rows, err := db.QueryContext(ctx, query, a.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []catalogRecord{}
	for rows.Next() {
		var meta, keys, division []byte
		var record catalogRecord
		if err = rows.Scan(&meta, &keys, &division, &record.Parts, &record.Questions, &record.Assigned); err != nil {
			return nil, err
		}
		e := &Exam{}
		if err = json.Unmarshal(meta, e); err != nil {
			return nil, err
		}
		var ids []string
		if len(keys) > 0 {
			if err = json.Unmarshal(keys, &ids); err != nil {
				return nil, err
			}
		}
		e.Divisions = map[int64]*Division{}
		for _, raw := range ids {
			id, er := strconv.ParseInt(raw, 10, 64)
			if er != nil {
				return nil, er
			}
			e.Divisions[id] = &Division{ID: id}
		}
		if d := e.Divisions[p.DivisionID]; d != nil {
			if err = json.Unmarshal(division, d); err != nil {
				return nil, err
			}
			d.ID = p.DivisionID
		}
		record.Exam = e
		out = append(out, record)
	}
	return out, rows.Err()
}

// Training reads need the live bank and one division, never stored attempts.
func loadTrainingBank(ctx context.Context, db *sql.DB, org, id, division int64) (*Exam, error) {
	path := fmt.Sprintf("$.divisions.\"%d\"", division)
	query := `SELECT JSON_SET(` + catalogProjection() + `,'$.parts',JSON_EXTRACT(document,'$.parts'),'$.questions',JSON_EXTRACT(document,'$.questions'),'$.divisions',IF(JSON_CONTAINS_PATH(document,'one',?),JSON_OBJECT(?,JSON_OBJECT('division_id',?,'revision',JSON_EXTRACT(document,'` + path + `.revision'),'date',JSON_EXTRACT(document,'` + path + `.date'),'config',JSON_EXTRACT(document,'` + path + `.config'),'training_enabled',JSON_EXTRACT(document,'` + path + `.training_enabled'))),JSON_OBJECT())) FROM exams_catalog_v1 WHERE organization_id=? AND id=?`
	var raw []byte
	if err := db.QueryRowContext(ctx, query, path, strconv.FormatInt(division, 10), division, org, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, Missing
		}
		return nil, err
	}
	e := &Exam{}
	if err := json.Unmarshal(raw, e); err != nil {
		return nil, err
	}
	return e, nil
}

func loadExamMetadata(ctx context.Context, db *sql.DB, org, id int64) (*Exam, error) {
	var raw, keys []byte
	err := db.QueryRowContext(ctx, `SELECT `+catalogProjection()+`,JSON_KEYS(JSON_EXTRACT(document,'$.divisions')) FROM exams_catalog_v1 WHERE organization_id=? AND id=?`, org, id).Scan(&raw, &keys)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, Missing
		}
		return nil, err
	}
	e := &Exam{}
	if err = json.Unmarshal(raw, e); err != nil {
		return nil, err
	}
	e.Divisions = map[int64]*Division{}
	var ids []string
	if len(keys) > 0 {
		if err = json.Unmarshal(keys, &ids); err != nil {
			return nil, err
		}
	}
	for _, key := range ids {
		id, er := strconv.ParseInt(key, 10, 64)
		if er != nil {
			return nil, er
		}
		e.Divisions[id] = &Division{ID: id}
	}
	return e, nil
}
