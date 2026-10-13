package cards

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	operationGrantHeader = "X-CPM-Operation-Grant"
	operationStageHeader = "X-CPM-Operation-Stage"
	operationJobHeader   = "X-CPM-Operation-Job"
	operationKeyHeader   = "X-CPM-Operation-Service-Key"
)

type operationRequest struct {
	GrantID   string
	Stage     string
	JobID     string
	NativeKey string
	SessionID string
	Body      []byte
	BodyHash  string
	Revision  int64
}

func hasOperationCarrier(r *http.Request) bool {
	for key := range r.Header {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "x-cpm-operation-") || strings.HasPrefix(key, "x-operation-") {
			return true
		}
	}
	return false
}

func singleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	return r.Header.Get(name), len(values) == 1 && values[0] != "" && strings.TrimSpace(values[0]) == values[0]
}

// Parse the actual immutable request without making an authorization decision.
// This must run before native Identify or any owner/dependency database access.
func parseOperationRequest(r *http.Request, c Config) (*operationRequest, error) {
	if !hasOperationCarrier(r) {
		return nil, nil
	}
	if len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("X-Service-Key")) != 0 {
		return nil, denied
	}
	for _, name := range []string{"X-Role", "X-Organization-ID", "X-Division-ID", "X-Account-ID", "X-User-ID", "X-Actor-ID", "X-Direction-ID", "X-CPM-Organization-ID", "X-CPM-Division-ID", "X-CPM-Actor-ID"} {
		if len(r.Header.Values(name)) != 0 {
			return nil, bad("invalid_operation_carrier")
		}
	}
	allowedHeaders := map[string]bool{}
	for _, name := range []string{operationGrantHeader, operationStageHeader, operationJobHeader, operationKeyHeader} {
		allowedHeaders[strings.ToLower(name)] = true
	}
	for name := range r.Header {
		lower := strings.ToLower(name)
		if (strings.HasPrefix(lower, "x-cpm-operation-") || strings.HasPrefix(lower, "x-operation-")) && !allowedHeaders[lower] {
			return nil, bad("invalid_operation_carrier")
		}
	}
	if c.validateOperationKeys() != nil || c.OperationAuthKey == "" {
		return nil, &APIError{503, "operation_authorization_unavailable"}
	}
	key, ok := singleHeader(r, operationKeyHeader)
	if !ok || subtle.ConstantTimeCompare([]byte(key), []byte(c.OperationDeliveryKey)) != 1 {
		return nil, denied
	}
	grant, grantOK := singleHeader(r, operationGrantHeader)
	job, jobOK := singleHeader(r, operationJobHeader)
	stage, stageOK := singleHeader(r, operationStageHeader)
	native, nativeOK := singleHeader(r, "Idempotency-Key")
	if !grantOK || !jobOK || !stageOK || !nativeOK || !lowerHex(grant, 32) || !lowerHex(job, 32) {
		return nil, bad("invalid_operation_carrier")
	}
	if !operationUUID(native) {
		return nil, bad("invalid_idempotency_key")
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.EscapedPath() != r.URL.Path {
		return nil, bad("invalid_operation_path")
	}
	var session string
	const prefix = "/v1/cards/sessions/"
	switch stage {
	case "apply":
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, "/commit") {
			return nil, denied
		}
		session = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/commit")
		if !lowerHex(session, 32) || r.URL.Path != prefix+session+"/commit" {
			return nil, denied
		}
	case "preview":
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, prefix) {
			return nil, denied
		}
		session = strings.TrimPrefix(r.URL.Path, prefix)
		if !lowerHex(session, 32) {
			return nil, denied
		}
	case "receipt":
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cards/receipts/"+native {
			return nil, denied
		}
	default:
		return nil, denied
	}
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
		if err != nil || len(body) > 16<<10 {
			return nil, bad("invalid_operation_body")
		}
	}
	if stage != "apply" && len(body) != 0 {
		return nil, bad("invalid_operation_body")
	}
	var revision int64
	if stage == "apply" {
		if len(r.Header.Values("Content-Type")) != 1 || strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
			return nil, &APIError{415, "json_required"}
		}
		var err error
		revision, err = operationRevision(body)
		if err != nil {
			return nil, err
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	digest := sha256.Sum256(body)
	return &operationRequest{grant, stage, job, native, session, body, hex.EncodeToString(digest[:]), revision}, nil
}

func operationUUID(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' && lowerHex(strings.ReplaceAll(s, "-", ""), 32)
}

func operationRevision(body []byte) (int64, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return 0, bad("invalid_operation_body")
	}
	key, err := d.Token()
	if err != nil || key != "expected_revision" {
		return 0, bad("invalid_operation_body")
	}
	var revision int64
	if d.Decode(&revision) != nil || revision < 1 || d.More() {
		return 0, bad("invalid_operation_body")
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return 0, bad("invalid_operation_body")
	}
	return revision, nil
}
