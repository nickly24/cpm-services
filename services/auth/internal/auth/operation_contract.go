package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	operationHex32          = regexp.MustCompile(`^[0-9a-f]{32}$`)
	operationHex24          = regexp.MustCompile(`^[0-9a-f]{24}$`)
	operationSHA            = regexp.MustCompile(`^[0-9a-f]{64}$`)
	operationUUID           = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	operationCoordinatorKey = regexp.MustCompile(`^[a-zA-Z0-9:_-]{8,128}$`)
)

type OperationProof struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      *string `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}

type OperationBinding struct {
	ContractVersion       int               `json:"contract_version"`
	UploadID              string            `json:"upload_id"`
	UploadSessionID       string            `json:"upload_session_id"`
	JobID                 string            `json:"job_id"`
	CoordinatorCommandKey string            `json:"coordinator_command_key"`
	Type                  string            `json:"type"`
	Owner                 string            `json:"owner"`
	NativeCommandKey      string            `json:"native_command_key"`
	OwnerSessionID        *string           `json:"owner_session_id"`
	UploadPreviewRevision string            `json:"upload_preview_revision"`
	OwnerPreviewRevision  *string           `json:"owner_preview_revision"`
	TargetRevision        *string           `json:"target_revision"`
	Target                map[string]string `json:"target"`
	SourceSHA256          string            `json:"source_sha256"`
	OwnerProof            OperationProof    `json:"owner_proof"`
	OwnerRequestSHA256    string            `json:"owner_request_sha256"`
	ApplyWireBodySHA256   string            `json:"apply_wire_body_sha256"`
	PreviewWireBodySHA256 *string           `json:"preview_wire_body_sha256"`
	UploadExpiresAt       string            `json:"upload_expires_at"`
	OwnerPreviewExpiresAt *string           `json:"owner_preview_expires_at"`
	RequiredDivisionIDs   []string          `json:"required_division_ids"`
}

type OperationAuthorizeRequest struct {
	ContractVersion  int    `json:"contract_version"`
	JobID            string `json:"job_id"`
	Stage            string `json:"stage"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	RawQuery         string `json:"raw_query"`
	ForceQuery       bool   `json:"force_query"`
	WireBodySHA256   string `json:"wire_body_sha256"`
	NativeCommandKey string `json:"native_command_key"`
}

type OperationActor struct {
	ID                int64                 `json:"id"`
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        int64                 `json:"division_id"`
	DivisionIDs       []int64               `json:"division_ids"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

type OperationDivisionPermissions struct {
	DivisionID  int64                 `json:"division_id"`
	Permissions map[string]Permission `json:"permissions"`
}

type OperationResponse struct {
	Status              bool                           `json:"status"`
	ContractVersion     int                            `json:"contract_version"`
	GrantID             string                         `json:"grant_id"`
	Mode                string                         `json:"mode"`
	ExpiresAt           string                         `json:"expires_at"`
	Actor               OperationActor                 `json:"actor"`
	DivisionPermissions []OperationDivisionPermissions `json:"division_permissions"`
	Binding             OperationBinding               `json:"binding"`
}

type operationType struct{ owner, section, scheme string }

var operationTypes = map[string]operationType{
	"users.credentials":      {"core", "users", "core.credentials.snapshot.v1"},
	"users.legacy":           {"core", "users", "core.legacy.snapshot.v1"},
	"cards.xlsx":             {"cards", "train", "cards.xlsx.v1"},
	"tests.json":             {"tests", "tests", "tests.json.v1"},
	"tests.external-results": {"tests", "test-results", "tests.external-results.v1"},
	"exams.questions":        {"exams", "exams", "exams.import.v1"},
	"exams.assignments":      {"exams", "exams", "exams.import.v1"},
	"exams.outside":          {"exams", "exams", "exams.import.v1"},
}

type operationError struct {
	status int
	code   string
}

func (e *operationError) Error() string     { return e.code }
func opError(status int, code string) error { return &operationError{status, code} }

func operationDecimal(s string) (int64, bool) {
	if s == "" || s[0] == '0' {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, e := strconv.ParseInt(s, 10, 64)
	return n, e == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
func operationDecimalPtr(s *string) bool {
	if s == nil {
		return false
	}
	_, ok := operationDecimal(*s)
	return ok
}
func operationSHAptr(s *string) bool { return s != nil && operationSHA.MatchString(*s) }
func operationUTC(s string) (time.Time, bool) {
	t, e := time.Parse(time.RFC3339Nano, s)
	return t, e == nil && strings.HasSuffix(s, "Z") && t.Location() == time.UTC
}
func operationDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (b OperationBinding) validate(originDivision int64) error {
	bad := opError(400, "invalid_operation_request")
	t, known := operationTypes[b.Type]
	if b.ContractVersion != 1 || !known || b.Owner != t.owner || !operationHex32.MatchString(b.UploadID) || !operationHex32.MatchString(b.UploadSessionID) || !operationHex32.MatchString(b.JobID) || !operationCoordinatorKey.MatchString(b.CoordinatorCommandKey) || !operationUUID.MatchString(b.NativeCommandKey) {
		return bad
	}
	if _, ok := operationDecimal(b.UploadPreviewRevision); !ok {
		return bad
	}
	if !operationSHA.MatchString(b.SourceSHA256) || !operationSHA.MatchString(b.OwnerRequestSHA256) || !operationSHA.MatchString(b.ApplyWireBodySHA256) || b.OwnerProof.Scheme != t.scheme || !operationSHA.MatchString(b.OwnerProof.ValidationSHA256) {
		return bad
	}
	if b.OwnerProof.InputSHA256 != nil && !operationSHAptr(b.OwnerProof.InputSHA256) || b.OwnerProof.MappingSHA256 != nil && !operationSHAptr(b.OwnerProof.MappingSHA256) {
		return bad
	}
	if _, ok := operationUTC(b.UploadExpiresAt); !ok {
		return bad
	}
	if b.OwnerPreviewExpiresAt != nil {
		if _, ok := operationUTC(*b.OwnerPreviewExpiresAt); !ok {
			return bad
		}
	}
	if len(b.RequiredDivisionIDs) == 0 || len(b.RequiredDivisionIDs) > 1000 {
		return bad
	}
	previous := int64(0)
	foundOrigin := false
	for _, id := range b.RequiredDivisionIDs {
		n, ok := operationDecimal(id)
		if !ok || n <= previous {
			return bad
		}
		previous = n
		foundOrigin = foundOrigin || n == originDivision
	}
	if !foundOrigin || b.Type != "exams.questions" && len(b.RequiredDivisionIDs) != 1 {
		return bad
	}
	if b.Type == "tests.json" || b.Type == "tests.external-results" {
		if b.OwnerSessionID != nil || b.OwnerPreviewRevision != nil || b.OwnerPreviewExpiresAt != nil || !operationSHAptr(b.PreviewWireBodySHA256) || !operationSHAptr(b.OwnerProof.InputSHA256) {
			return bad
		}
	} else {
		if b.OwnerSessionID == nil || b.PreviewWireBodySHA256 != nil {
			return bad
		}
		if b.Type == "users.credentials" {
			if b.OwnerPreviewRevision != nil || b.OwnerPreviewExpiresAt != nil {
				return bad
			}
		} else if !operationDecimalPtr(b.OwnerPreviewRevision) || b.OwnerPreviewExpiresAt == nil {
			return bad
		}
	}
	if b.Type != "tests.external-results" && b.OwnerProof.MappingSHA256 != nil {
		return bad
	}
	if b.Type != "tests.external-results" && !strings.HasPrefix(b.Type, "exams.") && b.TargetRevision != nil {
		return bad
	}
	if strings.HasPrefix(b.Type, "users.") {
		if !operationDecimalPtr(b.OwnerSessionID) || !operationSHAptr(b.OwnerProof.InputSHA256) || b.TargetRevision != nil || !operationTarget(b.Target, "division_users", "division_id") || b.Target["division_id"] != strconv.FormatInt(originDivision, 10) {
			return bad
		}
	} else if b.Type == "cards.xlsx" {
		if !operationHex32.MatchString(*b.OwnerSessionID) || b.OwnerProof.InputSHA256 != nil || b.TargetRevision != nil {
			return bad
		}
		if b.Target["kind"] == "existing_theme" {
			if !operationTarget(b.Target, "existing_theme", "direction_id", "theme_id") {
				return bad
			}
			if _, ok := operationDecimal(b.Target["theme_id"]); !ok {
				return bad
			}
		} else if b.Target["kind"] == "create_theme" {
			if !operationTarget(b.Target, "create_theme", "direction_id", "name", "collision_policy") || b.Target["collision_policy"] != "reuse_existing" {
				return bad
			}
			name := b.Target["name"]
			if name == "" || name != strings.TrimSpace(name) || !utf8.ValidString(name) || len(name) > 2048 {
				return bad
			}
		} else {
			return bad
		}
		if _, ok := operationDecimal(b.Target["direction_id"]); !ok {
			return bad
		}
	} else if b.Type == "tests.json" {
		if b.TargetRevision != nil || !operationTarget(b.Target, "create_tests", "direction_id") {
			return bad
		}
		if _, ok := operationDecimal(b.Target["direction_id"]); !ok {
			return bad
		}
	} else if b.Type == "tests.external-results" {
		if !operationDecimalPtr(b.TargetRevision) || !operationSHAptr(b.OwnerProof.MappingSHA256) || !operationTarget(b.Target, "external_test", "test_id") || !operationHex24.MatchString(b.Target["test_id"]) {
			return bad
		}
	} else {
		if !operationUUID.MatchString(*b.OwnerSessionID) || !operationDecimalPtr(b.TargetRevision) || b.OwnerProof.InputSHA256 != nil || !operationTarget(b.Target, "exam", "exam_id") {
			return bad
		}
		if _, ok := operationDecimal(b.Target["exam_id"]); !ok {
			return bad
		}
	}
	return nil
}

func operationTarget(target map[string]string, kind string, fields ...string) bool {
	if len(target) != len(fields)+1 || target["kind"] != kind {
		return false
	}
	for _, field := range fields {
		if target[field] == "" {
			return false
		}
	}
	return true
}

func (b OperationBinding) requestMatches(originDivision int64, r OperationAuthorizeRequest) bool {
	if r.ContractVersion != 1 || r.JobID != b.JobID || r.NativeCommandKey != b.NativeCommandKey || r.ForceQuery || !operationSHA.MatchString(r.WireBodySHA256) {
		return false
	}
	method, path, body := "", "", ""
	if r.Stage == "core-direction" {
		if b.Type != "cards.xlsx" {
			return false
		}
		method, path, body = "GET", "/internal/core/import-directions/"+b.Target["direction_id"], operationDigest(nil)
	} else {
		base := ""
		switch b.Owner {
		case "core":
			family := "imports"
			if b.Type == "users.legacy" {
				family = "legacy-imports"
			}
			base = "/v1/core/divisions/" + strconv.FormatInt(originDivision, 10) + "/" + family + "/" + *b.OwnerSessionID
		case "cards":
			base = "/v1/cards/sessions/" + *b.OwnerSessionID
		case "tests":
			base = "/internal/tests/imports/json"
			if b.Type == "tests.external-results" {
				base = "/internal/tests/external-results"
			}
		case "exams":
			base = "/v1/exams/" + b.Target["exam_id"] + "/imports/" + strings.TrimPrefix(b.Type, "exams.") + "/" + *b.OwnerSessionID
		default:
			return false
		}
		switch r.Stage {
		case "apply":
			method, path, body = "POST", base+"/commit", b.ApplyWireBodySHA256
		case "preview":
			method, path, body = "GET", base, operationDigest(nil)
			if b.Owner == "tests" {
				method, path, body = "POST", base+"/validate", *b.PreviewWireBodySHA256
			}
		case "receipt":
			method, path, body = "GET", base+"/receipts/"+b.NativeCommandKey, operationDigest(nil)
			if b.Owner == "cards" {
				path = "/v1/cards/receipts/" + b.NativeCommandKey
			}
			if b.Owner == "tests" {
				method, path, body = "POST", base+"/receipt", b.ApplyWireBodySHA256
			}
		default:
			return false
		}
	}
	if r.Method != method || r.Path != path || r.WireBodySHA256 != body {
		return false
	}
	if b.Owner == "exams" && r.Stage == "preview" {
		q, e := url.ParseQuery(r.RawQuery)
		if e != nil || len(q) != 3 || q.Encode() != r.RawQuery || len(q["expected_revision"]) != 1 || len(q["limit"]) != 1 || len(q["offset"]) != 1 || q.Get("expected_revision") != *b.OwnerPreviewRevision {
			return false
		}
		limit, ok := operationDecimal(q.Get("limit"))
		if !ok || limit > 200 {
			return false
		}
		offset, e := strconv.ParseInt(q.Get("offset"), 10, 64)
		return e == nil && offset >= 0 && offset <= 5000 && strconv.FormatInt(offset, 10) == q.Get("offset")
	}
	return r.RawQuery == ""
}
