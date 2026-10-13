package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const externalImportMaxRows = 5000

// Uploads owns files and preview sessions. These DTOs contain only the domain
// rows that Tests is allowed to validate and confirm in its own aggregate.
type ExternalImportRowDTO struct {
	RowID           string   `json:"row_id"`
	Row             int64    `json:"row"`
	FullName        string   `json:"full_name"`
	AccountID       *int64   `json:"account_id,omitempty"`
	LegacyStudentID *int64   `json:"legacy_student_id,omitempty"`
	Percent         *float64 `json:"percent"`
	CorrectCount    *int64   `json:"correct_count,omitempty"`
	CompletedAt     string   `json:"completed_at,omitempty"`
	TimeSpent       string   `json:"time_spent,omitempty"`
	Login           string   `json:"login,omitempty"`
}

type ExternalImportValidateDTO struct {
	OrganizationID       int64                  `json:"organization_id"`
	DivisionID           int64                  `json:"division_id"`
	TestID               string                 `json:"test_id"`
	ExpectedTestRevision int64                  `json:"expected_test_revision"`
	Rows                 []ExternalImportRowDTO `json:"rows"`
}

type ExternalImportCommitDTO struct {
	ExternalImportValidateDTO
	CommandID             string `json:"command_id"`
	PreviewHash           string `json:"preview_hash"`
	UploadID              string `json:"upload_id"`
	UploadSessionID       string `json:"upload_session_id"`
	UploadPreviewRevision int64  `json:"upload_preview_revision"`
}

type ExternalImportRowError struct {
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ExternalImportCandidate struct {
	AccountID       int64  `json:"account_id"`
	LegacyStudentID int64  `json:"legacy_student_id"`
	FullName        string `json:"full_name"`
}

type ExternalImportPreviewRow struct {
	ExternalImportRowDTO
	StudentFullName string                    `json:"student_full_name,omitempty"`
	Action          string                    `json:"action"`
	Errors          []ExternalImportRowError  `json:"errors"`
	Candidates      []ExternalImportCandidate `json:"candidates,omitempty"`
}

type ExternalImportSummary struct {
	TotalRows       int `json:"total_rows"`
	ImportRows      int `json:"import_rows"`
	RowErrors       int `json:"row_errors"`
	MatchedStudents int `json:"matched_students"`
	DuplicateRows   int `json:"duplicate_rows"`
	ExistingResults int `json:"existing_results"`
}

type ExternalImportPreview struct {
	OrganizationID int64                      `json:"organization_id"`
	DivisionID     int64                      `json:"division_id"`
	TestID         string                     `json:"test_id"`
	Revision       int64                      `json:"revision"`
	PreviewHash    string                     `json:"preview_hash"`
	Rows           []ExternalImportPreviewRow `json:"rows"`
	Summary        ExternalImportSummary      `json:"summary"`
	CanCommit      bool                       `json:"can_commit"`
	OwnerProof     *ImportOwnerProof          `json:"owner_proof,omitempty"`
}

type ExternalImportCommitResult struct {
	CommandID      string                  `json:"command_id"`
	Status         string                  `json:"status"`
	SourceRevision int64                   `json:"source_revision"`
	Results        []ExternalResultReceipt `json:"results"`
}

func externalImportPersonKey(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return ""
	}
	// Preserve legacy name matching: lowercase surname and given name only.
	// In particular, do not invent ё/е substitutions or compare the patronymic.
	return cases.Lower(language.Und).String(parts[0] + "|" + parts[1])
}

func externalImportNormalizedRows(rows []ExternalImportRowDTO) []ExternalImportRowDTO {
	out := make([]ExternalImportRowDTO, len(rows))
	for i, row := range rows {
		out[i] = row
		out[i].RowID = strings.TrimSpace(row.RowID)
		out[i].FullName = strings.Join(strings.Fields(row.FullName), " ")
		out[i].CompletedAt = strings.TrimSpace(row.CompletedAt)
		out[i].TimeSpent = strings.TrimSpace(row.TimeSpent)
		out[i].Login = strings.TrimSpace(row.Login)
		if row.Percent != nil {
			value := *row.Percent
			if value == 0 {
				value = 0 // Canonicalize negative zero without rounding decimals.
			}
			out[i].Percent = &value
		}
		if row.AccountID != nil {
			value := *row.AccountID
			out[i].AccountID = &value
		}
		if row.LegacyStudentID != nil {
			value := *row.LegacyStudentID
			out[i].LegacyStudentID = &value
		}
		if row.CorrectCount != nil {
			value := *row.CorrectCount
			out[i].CorrectCount = &value
		}
	}
	return out
}

func externalImportObjectID(raw string) (bson.ObjectID, error) {
	id, err := bson.ObjectIDFromHex(strings.TrimPrefix(strings.TrimSpace(raw), "external_"))
	if err != nil || id.IsZero() {
		return bson.ObjectID{}, bad("invalid_external_test_id")
	}
	return id, nil
}

func externalImportRequest(in ExternalImportValidateDTO) (ExternalImportValidateDTO, bson.ObjectID, error) {
	id, err := externalImportObjectID(in.TestID)
	if err != nil {
		return in, id, err
	}
	if in.OrganizationID < 1 || in.DivisionID < 1 || in.ExpectedTestRevision < 0 || len(in.Rows) < 1 || len(in.Rows) > externalImportMaxRows {
		return in, id, bad("invalid_external_import_request")
	}
	in.TestID = id.Hex()
	in.Rows = externalImportNormalizedRows(in.Rows)
	return in, id, nil
}

func externalImportHash(value any) string {
	sum := sha256.Sum256(canonicalJSON(value))
	return hex.EncodeToString(sum[:])
}

func externalImportBuildPreview(test ExternalTestDocument, rows []ExternalImportRowDTO, people []ExternalPerson) ExternalImportPreview {
	rows = externalImportNormalizedRows(rows)
	out := ExternalImportPreview{OrganizationID: test.OrganizationID, DivisionID: test.DivisionID,
		TestID: test.ID.Hex(), Revision: test.Revision, Rows: []ExternalImportPreviewRow{},
		Summary: ExternalImportSummary{TotalRows: len(rows)}}
	byName := map[string][]ExternalPerson{}
	byAccount := map[int64]ExternalPerson{}
	byStudent := map[int64][]ExternalPerson{}
	for _, person := range people {
		if person.AccountID < 1 || person.StudentID < 1 {
			continue
		}
		// Core already scoped this list to the organization and division. A
		// blocked/inactive pupil can still have historical external results.
		byAccount[person.AccountID] = person
		byStudent[person.StudentID] = append(byStudent[person.StudentID], person)
		if key := externalImportPersonKey(person.FullName); key != "" {
			byName[key] = append(byName[key], person)
		}
	}
	names, rowIDs, accounts := map[string]int{}, map[string]int{}, map[int64]int{}
	existing := map[int64]bool{}
	for _, result := range test.Results {
		existing[result.AccountID] = true
	}
	for _, row := range rows {
		if key := externalImportPersonKey(row.FullName); key != "" {
			names[key]++
		}
		rowIDs[row.RowID]++
	}
	for _, row := range rows {
		item := ExternalImportPreviewRow{ExternalImportRowDTO: row, Action: "import", Errors: []ExternalImportRowError{}}
		add := func(code, field, message string) {
			item.Errors = append(item.Errors, ExternalImportRowError{code, field, message})
		}
		key := externalImportPersonKey(row.FullName)
		if row.RowID == "" || row.Row < 1 {
			add("invalid_source_row", "row_id", "Укажите идентификатор и номер исходной строки")
		}
		if rowIDs[row.RowID] > 1 {
			add("duplicate_source_row", "row_id", "Идентификатор строки повторяется в наборе")
		}
		if key == "" {
			add("invalid_full_name", "full_name", "Укажите минимум фамилию и имя")
		} else if names[key] > 1 {
			add("duplicate_full_name", "full_name", "Фамилия и имя повторяются в наборе")
		}
		if row.Percent == nil || math.IsNaN(valueOrZero(row.Percent)) || math.IsInf(valueOrZero(row.Percent), 0) || *row.Percent < 0 || *row.Percent > 100 {
			add("invalid_percent", "percent", "Процент должен быть числом от 0 до 100 включительно")
			if row.Percent != nil && (math.IsNaN(*row.Percent) || math.IsInf(*row.Percent, 0)) {
				// Such a value cannot arrive as JSON, but an adapter calling this
				// validator must still receive a serializable error preview.
				item.Percent = nil
			}
		}
		var matches []ExternalPerson
		switch {
		case row.AccountID != nil:
			if *row.AccountID < 1 {
				add("invalid_account_id", "account_id", "Некорректный идентификатор аккаунта")
			} else if person, exists := byAccount[*row.AccountID]; exists {
				matches = []ExternalPerson{person}
			} else {
				add("student_not_in_scope", "account_id", "Ученик не найден в выбранном подразделении")
			}
		case row.LegacyStudentID != nil:
			if *row.LegacyStudentID < 1 {
				add("invalid_student_id", "legacy_student_id", "Некорректный идентификатор профиля ученика")
			} else {
				matches = byStudent[*row.LegacyStudentID]
			}
		case key != "" && names[key] == 1:
			matches = byName[key]
		}
		if len(matches) == 1 {
			person := matches[0]
			if row.LegacyStudentID != nil && *row.LegacyStudentID != person.StudentID {
				add("student_account_mismatch", "legacy_student_id", "Профиль ученика не соответствует аккаунту")
			}
			account, student := person.AccountID, person.StudentID
			item.AccountID, item.LegacyStudentID = &account, &student
			item.StudentFullName = person.FullName
			accounts[account]++
			if existing[account] {
				add("existing_result", "account_id", "У ученика уже есть результат по выбранному внешнему тесту")
			}
		} else if len(matches) > 1 {
			add("ambiguous_student", "full_name", "Найдено несколько учеников; выберите аккаунт явно")
			sort.Slice(matches, func(i, j int) bool { return matches[i].AccountID < matches[j].AccountID })
			for _, person := range matches {
				item.Candidates = append(item.Candidates, ExternalImportCandidate{person.AccountID, person.StudentID, person.FullName})
			}
		} else if key != "" && names[key] == 1 && row.AccountID == nil && (row.LegacyStudentID == nil || *row.LegacyStudentID > 0) {
			add("student_not_found", "full_name", "Ученик не найден в выбранном подразделении")
		}
		out.Rows = append(out.Rows, item)
	}
	for i := range out.Rows {
		item := &out.Rows[i]
		if item.AccountID != nil && accounts[*item.AccountID] > 1 {
			item.Errors = append(item.Errors, ExternalImportRowError{"duplicate_account", "account_id", "Аккаунт ученика повторяется в наборе"})
		}
		if len(item.Errors) == 0 {
			out.Summary.ImportRows++
			out.Summary.MatchedStudents++
		} else {
			item.Action = "error"
			out.Summary.RowErrors++
		}
		for _, problem := range item.Errors {
			if problem.Code == "duplicate_full_name" || problem.Code == "duplicate_account" || problem.Code == "duplicate_source_row" {
				out.Summary.DuplicateRows++
				break
			}
		}
		for _, problem := range item.Errors {
			if problem.Code == "existing_result" {
				out.Summary.ExistingResults++
				break
			}
		}
	}
	out.CanCommit = out.Summary.ImportRows > 0 && out.Summary.RowErrors == 0
	out.PreviewHash = externalImportHash(out)
	return out
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func externalImportValidUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 {
		return false
	}
	for _, part := range decoded {
		if part != 0 {
			return true
		}
	}
	return false
}

func externalImportCommitRequest(in ExternalImportCommitDTO, actorID int64) (ExternalImportCommitDTO, bson.ObjectID, string, error) {
	normalized, id, err := externalImportRequest(in.ExternalImportValidateDTO)
	if err != nil {
		return in, id, "", err
	}
	in.ExternalImportValidateDTO = normalized
	in.CommandID = strings.ToLower(strings.TrimSpace(in.CommandID))
	in.PreviewHash = strings.ToLower(strings.TrimSpace(in.PreviewHash))
	in.UploadID, in.UploadSessionID = strings.TrimSpace(in.UploadID), strings.TrimSpace(in.UploadSessionID)
	preview, hashErr := hex.DecodeString(in.PreviewHash)
	if actorID < 1 || !externalImportValidUUID(in.CommandID) || hashErr != nil || len(preview) != sha256.Size || in.ExpectedTestRevision < 1 || in.UploadID == "" || in.UploadSessionID == "" || in.UploadPreviewRevision < 1 {
		return in, id, "", bad("invalid_external_import_command")
	}
	for _, row := range in.Rows {
		if row.Percent != nil && (math.IsNaN(*row.Percent) || math.IsInf(*row.Percent, 0)) {
			return in, id, "", bad("invalid_external_import_command")
		}
	}
	inputHash := externalImportHash(struct {
		ActorID int64 `json:"actor_id"`
		ExternalImportCommitDTO
	}{actorID, in})
	return in, id, inputHash, nil
}

func externalImportReplay(test ExternalTestDocument, commandID, inputHash string, actorID int64) (ExternalCommandReceipt, bool, error) {
	for _, receipt := range test.Commands {
		if receipt.CommandID == commandID {
			if receipt.InputHash != inputHash || receipt.ActorID != actorID {
				return ExternalCommandReceipt{}, false, conflict("external_import_idempotency_conflict")
			}
			return receipt, true, nil
		}
	}
	return ExternalCommandReceipt{}, false, nil
}

func externalImportReceiptResult(receipt ExternalCommandReceipt) ExternalImportCommitResult {
	return ExternalImportCommitResult{receipt.CommandID, "committed", receipt.SourceRevision, receipt.Results}
}

func externalImportPlan(test ExternalTestDocument, in ExternalImportCommitDTO, inputHash string, actorID int64, people []ExternalPerson) (ExternalTestDocument, ExternalCommandReceipt, error) {
	if test.Deleted || test.OrganizationID != in.OrganizationID || test.DivisionID != in.DivisionID || test.ID.Hex() != in.TestID {
		return ExternalTestDocument{}, ExternalCommandReceipt{}, missing
	}
	if test.Revision != in.ExpectedTestRevision {
		return ExternalTestDocument{}, ExternalCommandReceipt{}, conflict("external_revision_conflict")
	}
	preview := externalImportBuildPreview(test, in.Rows, people)
	if !preview.CanCommit {
		return ExternalTestDocument{}, ExternalCommandReceipt{}, bad("external_import_rows_invalid")
	}
	if preview.PreviewHash != in.PreviewHash {
		return ExternalTestDocument{}, ExternalCommandReceipt{}, conflict("external_import_preview_changed")
	}
	after := test
	after.Results = append([]ExternalResult{}, test.Results...)
	after.Commands = append([]ExternalCommandReceipt{}, test.Commands...)
	receipt := ExternalCommandReceipt{CommandID: in.CommandID, InputHash: inputHash, ActorID: actorID,
		SourceRevision: test.Revision + 1, Results: []ExternalResultReceipt{}, UploadID: in.UploadID,
		UploadSessionID: in.UploadSessionID, UploadPreviewRevision: in.UploadPreviewRevision}
	for _, row := range preview.Rows {
		result := ExternalResult{ID: bson.NewObjectID(), Revision: 1, AccountID: *row.AccountID,
			StudentID: *row.LegacyStudentID, FullName: row.StudentFullName, Score: row.Percent,
			CorrectCount: row.CorrectCount, CompletedAt: row.CompletedAt, TimeSpent: row.TimeSpent,
			Login: row.Login, SourceRowID: row.RowID}
		after.Results = append(after.Results, result)
		receipt.Results = append(receipt.Results, ExternalResultReceipt{RowID: row.RowID, ResultID: result.ID,
			AccountID: result.AccountID, Revision: result.Revision, Percent: *result.Score})
	}
	after.Commands = append(after.Commands, receipt)
	return after, receipt, nil
}

func (s *Server) externalValidate(r *http.Request) (any, int, error) {
	actor, err := s.identifyImport(r)
	if err != nil {
		return nil, 0, err
	}
	var raw ExternalImportValidateDTO
	if err = decode(r, &raw); err != nil {
		return nil, 0, err
	}
	in, id, err := externalImportRequest(raw)
	if err != nil {
		return nil, 0, err
	}
	if err := checkExternalImportBinding(r, actor, in, "", ""); err != nil {
		return nil, 0, err
	}
	if err = s.externalImportAccess(r, actor, in.OrganizationID, in.DivisionID, false); err != nil {
		return nil, 0, err
	}
	test, err := s.Store.ExternalGet(r.Context(), in.OrganizationID, id)
	if err != nil {
		return nil, 0, err
	}
	if test.Deleted || test.DivisionID != in.DivisionID {
		return nil, 0, missing
	}
	if in.ExpectedTestRevision != 0 && test.Revision != in.ExpectedTestRevision {
		return nil, 0, conflict("external_revision_conflict")
	}
	people, err := s.externalPeople(r.Context(), in.OrganizationID, in.DivisionID)
	if err != nil {
		return nil, 0, err
	}
	preview := externalImportBuildPreview(test, in.Rows, people)
	preview.OwnerProof = externalImportOwnerProof(in, preview)
	if e := checkImportOwnerProof(r, preview.OwnerProof); e != nil {
		return nil, 0, e
	}
	return preview, http.StatusOK, nil
}

func (s *Server) externalCommit(r *http.Request) (any, int, error) {
	actor, err := s.identifyImport(r)
	if err != nil {
		return nil, 0, err
	}
	var raw ExternalImportCommitDTO
	if err = decode(r, &raw); err != nil {
		return nil, 0, err
	}
	in, id, inputHash, err := externalImportCommitRequest(raw, actor.AccountID)
	if err != nil {
		return nil, 0, err
	}
	if err := checkExternalImportBinding(r, actor, in.ExternalImportValidateDTO, in.CommandID, inputHash); err != nil {
		return nil, 0, err
	}
	if err := checkExternalCommandBinding(r, in); err != nil {
		return nil, 0, err
	}
	// Idempotency is not authorization. Recheck the live actor and scope even
	// when this command was already confirmed by a previous HTTP request.
	if err = s.externalImportAccess(r, actor, in.OrganizationID, in.DivisionID, true); err != nil {
		return nil, 0, err
	}
	test, err := s.Store.ExternalGet(r.Context(), in.OrganizationID, id)
	if err != nil {
		return nil, 0, err
	}
	if test.DivisionID != in.DivisionID {
		return nil, 0, missing
	}
	receipt, replay, err := externalImportReplay(test, in.CommandID, inputHash, actor.AccountID)
	if err != nil {
		return nil, 0, err
	}
	if replay {
		return externalImportReceiptResult(receipt), http.StatusOK, nil
	}
	if test.Deleted {
		return nil, 0, missing
	}
	people, err := s.externalPeople(r.Context(), in.OrganizationID, in.DivisionID)
	if err != nil {
		return nil, 0, err
	}
	if importOperationContext(r) != nil {
		preview := externalImportBuildPreview(test, in.Rows, people)
		if err := checkImportOwnerProof(r, externalImportOwnerProof(in.ExternalImportValidateDTO, preview)); err != nil {
			return nil, 0, err
		}
	}
	after, receipt, err := externalImportPlan(test, in, inputHash, actor.AccountID, people)
	if err != nil {
		return nil, 0, err
	}
	if _, err = s.externalSave(r.Context(), test, after, actor, "results_imported"); err != nil {
		// Two identical concurrent requests may both validate before the CAS.
		// Read the winner's durable receipt, without rerunning or duplicating rows.
		if live, readErr := s.Store.ExternalGet(r.Context(), in.OrganizationID, id); readErr == nil && live.DivisionID == in.DivisionID {
			if saved, found, replayErr := externalImportReplay(live, in.CommandID, inputHash, actor.AccountID); replayErr != nil {
				return nil, 0, replayErr
			} else if found {
				return externalImportReceiptResult(saved), http.StatusOK, nil
			}
		}
		return nil, 0, err
	}
	return externalImportReceiptResult(receipt), http.StatusCreated, nil
}
