package attendance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var commandUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CommandReceipt is positive commit evidence, not a view of current domain state.
type CommandReceipt struct {
	ContractVersion int     `json:"contract_version"`
	CommandID       string  `json:"command_id"`
	State           string  `json:"state"`
	OrganizationID  int64   `json:"organization_id"`
	Scope           string  `json:"scope"`
	DivisionIDs     []int64 `json:"division_ids"`
	Method          string  `json:"method"`
	Path            string  `json:"path"`
}

// This evidence is private to the existing SQL receipt JSON, never Result wire JSON.
type commandScope struct {
	Version     int     `json:"version"`
	Method      string  `json:"method"`
	Path        string  `json:"path"`
	Kind        string  `json:"kind"`
	Scope       string  `json:"scope"`
	DivisionIDs []int64 `json:"division_ids"`
}

type storedReceipt struct {
	Result
	ScopeMetadata *commandScope `json:"scope_metadata,omitempty"`
}

type commandRoute struct {
	Method, Path, Kind string
	Division           int64
	IDs                map[string]int64
}

// Match only actual write routes. In particular QR issuance is not a receipt command.
func originalCommand(action string) (commandRoute, error) {
	parts := strings.SplitN(action, " ", 2)
	if len(parts) != 2 || parts[0] != "POST" && parts[0] != "PUT" && parts[0] != "DELETE" || !strings.HasPrefix(parts[1], "/v1/attendance/") {
		return commandRoute{}, unavailable
	}
	actual := strings.Split(parts[1], "/")
	for _, route := range routes() {
		pattern := strings.SplitN(route.Pattern, " ", 2)
		if pattern[0] != parts[0] || route.Kind == "qr" {
			continue
		}
		chunks := strings.Split(pattern[1], "/")
		if len(chunks) != len(actual) {
			continue
		}
		ids := map[string]int64{}
		matches := true
		for i, chunk := range chunks {
			if strings.HasPrefix(chunk, "{") {
				name := strings.Trim(chunk, "{}")
				if name == "date" {
					matches = matches && validDate(actual[i])
				} else {
					n, e := strconv.ParseInt(actual[i], 10, 64)
					matches = matches && e == nil && n > 0
					ids[name] = n
				}
			} else if chunk != actual[i] {
				matches = false
			}
		}
		if matches {
			return commandRoute{parts[0], strings.TrimPrefix(parts[1], "/v1/attendance"), route.Kind, ids["division"], ids}, nil
		}
	}
	return commandRoute{}, unavailable
}

// Called only after write has validated and successfully applied the typed input.
func scopeAfterWrite(action, kind string, d int64, input json.RawMessage) (commandScope, error) {
	route, e := originalCommand(action)
	if e != nil || route.Kind != kind || route.Division != d {
		return commandScope{}, unavailable
	}
	meta := commandScope{Version: 1, Method: route.Method, Path: route.Path, Kind: kind, DivisionIDs: []int64{}}
	switch kind {
	case "room":
		meta.Scope = "organization"
	case "batch":
		meta.Scope = "divisions"
		var batch Batch
		if decodeJSON(input, &batch) != nil {
			return commandScope{}, unavailable
		}
		seen := map[int64]bool{}
		for _, lesson := range append(batch.Creates, batch.Updates...) {
			seen[lesson.DivisionID] = true
		}
		for _, lesson := range batch.Deletes {
			seen[lesson.DivisionID] = true
		}
		for division := range seen {
			meta.DivisionIDs = append(meta.DivisionIDs, division)
		}
		sort.Slice(meta.DivisionIDs, func(i, j int) bool { return meta.DivisionIDs[i] < meta.DivisionIDs[j] })
	default:
		meta.Scope = "division"
		meta.DivisionIDs = []int64{d}
	}
	if validateCommandScope(meta, route) != nil {
		return commandScope{}, unavailable
	}
	return meta, nil
}

func validateCommandScope(meta commandScope, route commandRoute) error {
	if meta.Version != 1 || meta.Method != route.Method || meta.Path != route.Path || meta.Kind != route.Kind || meta.DivisionIDs == nil || len(meta.DivisionIDs) > 100 {
		return unavailable
	}
	for i, d := range meta.DivisionIDs {
		if d < 1 || i > 0 && meta.DivisionIDs[i-1] >= d {
			return unavailable
		}
	}
	switch route.Kind {
	case "room":
		if meta.Scope != "organization" || len(meta.DivisionIDs) != 0 {
			return unavailable
		}
	case "batch":
		if meta.Scope != "divisions" || len(meta.DivisionIDs) == 0 {
			return unavailable
		}
	default:
		if meta.Scope != "division" || len(meta.DivisionIDs) != 1 || meta.DivisionIDs[0] != route.Division || route.Division < 1 {
			return unavailable
		}
	}
	return nil
}

type receiptEvidence struct {
	Result   Result
	Scope    commandScope
	Route    commandRoute
	Original *State
}

func (s Store) commandReceipt(ctx context.Context, a Identity, key string) (receiptEvidence, error) {
	var raw []byte
	e := s.DB.QueryRowContext(ctx, "SELECT response FROM attendance_receipts_v1 WHERE organization_id=? AND actor_account_id=? AND request_key=?", a.User.OrganizationID, a.User.AccountID, key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return receiptEvidence{}, missing
	}
	if e != nil {
		return receiptEvidence{}, unavailable
	}
	var stored struct {
		Revision int64           `json:"revision"`
		Value    json.RawMessage `json:"value"`
		Scope    json.RawMessage `json:"scope_metadata"`
	}
	if decodeJSON(raw, &stored) != nil || stored.Revision < 1 || len(stored.Value) == 0 || string(stored.Value) == "null" {
		return receiptEvidence{}, unavailable
	}
	var actor, revision int64
	var action string
	var snapshot []byte
	e = s.DB.QueryRowContext(ctx, "SELECT revision,actor_account_id,action,snapshot FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", a.User.OrganizationID, stored.Revision).Scan(&revision, &actor, &action, &snapshot)
	if e != nil || actor != a.User.AccountID || revision != stored.Revision {
		return receiptEvidence{}, unavailable
	}
	route, e := originalCommand(action)
	if e != nil {
		return receiptEvidence{}, e
	}
	var original State
	if decodeJSON(snapshot, &original) != nil || original.NextID < 1 || original.Calendars == nil || original.Lessons == nil || original.Rooms == nil || original.Types == nil || original.Days == nil || original.Entries == nil || original.Zaps == nil {
		return receiptEvidence{}, unavailable
	}
	meta := commandScope{Version: 1, Method: route.Method, Path: route.Path, Kind: route.Kind, DivisionIDs: []int64{}}
	if len(stored.Scope) > 0 {
		meta = commandScope{}
		if string(stored.Scope) == "null" || decodeJSON(stored.Scope, &meta) != nil || validateCommandScope(meta, route) != nil {
			return receiptEvidence{}, unavailable
		}
	} else if route.Kind == "batch" {
		return receiptEvidence{}, conflict("receipt_scope_unverifiable")
	} else if route.Kind == "room" {
		meta.Scope = "organization"
	} else {
		meta.Scope = "division"
		meta.DivisionIDs = []int64{route.Division}
	}
	evidence := receiptEvidence{Result{stored.Revision, stored.Value}, meta, route, &original}
	if e = validateJournalReceipt(evidence); e != nil {
		return receiptEvidence{}, e
	}
	return evidence, nil
}

// Current full-admin/staff rights do not excuse damaged original evidence.
// Both identity and historical group come from the immutable commit, never
// today's corrected entry or a newly supplied client target.
func validateJournalReceipt(e receiptEvidence) error {
	kind := e.Route.Kind
	if kind != "mark" && kind != "scan" && kind != "unmark" {
		return nil
	}
	b, err := json.Marshal(e.Result.Value)
	if err != nil {
		return unavailable
	}
	u := e.Route.IDs["account"]
	var resultEntry Entry
	if kind == "unmark" {
		var deleted Deleted
		if decodeJSON(b, &deleted) != nil || !deleted.Deleted {
			return unavailable
		}
	} else {
		if decodeJSON(b, &resultEntry) != nil || resultEntry.ID < 1 || resultEntry.DivisionID != e.Route.Division || resultEntry.DayID != e.Route.IDs["day"] || resultEntry.AccountID < 1 || resultEntry.Deleted {
			return unavailable
		}
		u = resultEntry.AccountID
	}
	original, ok := e.Original.Entries[key(e.Route.IDs["day"], u)]
	if !ok || original.ID < 1 || original.DivisionID != e.Route.Division || original.DayID != e.Route.IDs["day"] || original.AccountID != u || original.Deleted != (kind == "unmark") {
		return unavailable
	}
	if kind != "unmark" {
		before, _ := json.Marshal(original)
		after, _ := json.Marshal(resultEntry)
		if !bytes.Equal(before, after) {
			return unavailable
		}
	}
	return nil
}

func commandAllowed(a Identity, d int64, kind string) bool {
	switch kind {
	case "calendar", "lesson", "batch":
		return a.can(d, "schedule", true)
	case "type", "day":
		return a.can(d, "attendance", true)
	case "mark", "unmark":
		return a.journal(d, true)
	case "scan":
		return a.journal(d, true) || a.can(d, "scan", true)
	case "zap-create":
		return a.zapAccess(d, true)
	case "zap-process", "zap-retry", "zap-unlink":
		return a.can(d, "zaps", true)
	case "room":
		return a.admin()
	}
	return false
}

func (s *Server) authorizeCommandReceipt(r *http.Request, a Identity, evidence receiptEvidence) error {
	route := evidence.Route
	if route.Kind == "room" && !a.admin() {
		return denied
	}
	for _, d := range evidence.Scope.DivisionIDs {
		if !commandAllowed(a, d, route.Kind) {
			return denied
		}
		if _, e := s.Peers.Division(r.Context(), a.User.OrganizationID, d); e != nil {
			return e
		}
	}
	if route.Kind == "zap-create" {
		b, e := json.Marshal(evidence.Result.Value)
		var zap Zap
		if e != nil || decodeJSON(b, &zap) != nil || zap.ID < 1 || zap.DivisionID != route.Division || zap.AccountID < 1 {
			return unavailable
		}
		original, ok := evidence.Original.Zaps[zap.ID]
		if !ok || original.ID != zap.ID || original.DivisionID != zap.DivisionID || original.AccountID != zap.AccountID {
			return unavailable
		}
		if a.User.Role == "student" && zap.AccountID != a.User.AccountID {
			return denied
		}
	}
	if authorize := s.replayAuthorization(r, a, route.Division, route.IDs, route.Kind); authorize != nil {
		return authorize(evidence.Original, evidence.Result)
	}
	return nil
}

func (s *Server) readCommandReceipt(w http.ResponseWriter, r *http.Request, a Identity) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		fail(w, &APIError{405, "method_not_allowed"})
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(w, bad("invalid_receipt_request"))
		return
	}
	if r.Body != nil {
		b, e := io.ReadAll(io.LimitReader(r.Body, 1))
		if e != nil || len(b) != 0 {
			fail(w, bad("invalid_receipt_request"))
			return
		}
	}
	id := r.PathValue("command_id")
	if !commandUUID.MatchString(id) {
		fail(w, bad("invalid_command_id"))
		return
	}
	evidence, e := s.Store.commandReceipt(r.Context(), a, id)
	if e == nil {
		e = s.authorizeCommandReceipt(r, a, evidence)
	}
	if e != nil {
		fail(w, e)
		return
	}
	v := CommandReceipt{1, id, "committed", a.User.OrganizationID, evidence.Scope.Scope, evidence.Scope.DivisionIDs, evidence.Route.Method, evidence.Route.Path}
	reply(w, 200, map[string]any{"status": true, "data": Result{evidence.Result.Revision, v}})
}
