package auth

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

func operationCanonicalHTTP(r *http.Request) bool {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != r.URL.Path || strings.Contains(r.URL.Path, "//") {
		return false
	}
	for _, segment := range strings.Split(r.URL.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func operationSingleHeader(r *http.Request, name string) (string, bool) {
	values := []string{}
	for key, value := range r.Header {
		if strings.EqualFold(key, name) {
			values = append(values, value...)
		}
	}
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1 && returnValue != ""
}

func operationHeaderPresent(r *http.Request, name string) bool {
	for key := range r.Header {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func (s *Server) operationCredential(w http.ResponseWriter, r *http.Request, issuer bool) (string, bool) {
	if !s.Config.Operations.Enabled || s.Config.Operations.validate(s.Config.InternalKey) != nil {
		failure(w, 503, "authorization_unavailable")
		return "", false
	}
	for _, header := range []string{"X-CPM-Operation-Grant", "X-CPM-Operation-Stage", "X-CPM-Operation-Job", "X-CPM-Operation-Service-Key"} {
		if operationHeaderPresent(r, header) {
			failure(w, 401, "invalid_operation_credential")
			return "", false
		}
	}
	key, ok := operationSingleHeader(r, "X-Service-Key")
	if !ok {
		failure(w, 401, "invalid_operation_credential")
		return "", false
	}
	if issuer {
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.Config.Operations.IssuerKey)) != 1 {
			failure(w, 401, "invalid_operation_credential")
			return "", false
		}
		if _, ok := operationSingleHeader(r, "Authorization"); !ok {
			failure(w, 401, "invalid_token")
			return "", false
		}
		return "uploads", true
	}
	if operationHeaderPresent(r, "Authorization") {
		failure(w, 401, "invalid_operation_credential")
		return "", false
	}
	for owner, expected := range s.Config.Operations.OwnerKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(expected)) == 1 {
			return owner, true
		}
	}
	failure(w, 401, "invalid_operation_credential")
	return "", false
}

func readOperationBody(w http.ResponseWriter, r *http.Request, limit int64, target any) ([]byte, bool) {
	ct, ok := operationSingleHeader(r, "Content-Type")
	media, params, e := mime.ParseMediaType(ct)
	if !ok || e != nil || media != "application/json" || len(params) > 1 || len(params) == 1 && strings.ToLower(params["charset"]) != "utf-8" {
		failure(w, 415, "json_required")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	data, e := io.ReadAll(r.Body)
	if e != nil {
		var max *http.MaxBytesError
		if errors.As(e, &max) {
			failure(w, 413, "operation_request_too_large")
		} else {
			failure(w, 400, "invalid_operation_request")
		}
		return nil, false
	}
	if decodeOperationJSON(data, target) != nil {
		failure(w, 400, "invalid_operation_request")
		return nil, false
	}
	return data, true
}

func operationFailure(w http.ResponseWriter, e error) {
	var oe *operationError
	if errors.As(e, &oe) {
		failure(w, oe.status, oe.code)
		return
	}
	failure(w, 503, "authorization_unavailable")
}

func (s *Server) operationService() *OperationService {
	if s.Operations != nil {
		return s.Operations
	}
	return &OperationService{Store: SQLOperationStore{DB: s.Repo.DB}, Accounts: s.Repo, TTL: s.Config.Operations.TTL}
}

func (s *Server) issueOperation(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.operationCredential(w, r, true); !ok {
		return
	}
	key, ok := operationSingleHeader(r, "Idempotency-Key")
	if !ok || !operationUUID.MatchString(key) {
		failure(w, 400, "invalid_operation_request")
		return
	}
	var binding OperationBinding
	if _, ok := readOperationBody(w, r, 32768, &binding); !ok {
		return
	}
	a, _, u, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	response, e := s.operationService().Issue(r.Context(), a, u, key, binding)
	if e != nil {
		operationFailure(w, e)
		return
	}
	respond(w, 200, response)
}

func (s *Server) authorizeOperation(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.operationCredential(w, r, false)
	if !ok {
		return
	}
	var input OperationAuthorizeRequest
	if _, ok := readOperationBody(w, r, 4096, &input); !ok {
		return
	}
	response, e := s.operationService().Authorize(r.Context(), r.PathValue("grant"), owner, input)
	if e != nil {
		operationFailure(w, e)
		return
	}
	respond(w, 200, response)
}

func (s *Server) operationInitiator(w http.ResponseWriter, r *http.Request, issuance bool) (*Account, UserDTO, string, bool) {
	if _, ok := s.operationCredential(w, r, true); !ok {
		return nil, UserDTO{}, "", false
	}
	key := ""
	if issuance {
		var ok bool
		key, ok = operationSingleHeader(r, "Idempotency-Key")
		if !ok || !operationUUID.MatchString(key) {
			failure(w, 400, "invalid_operation_request")
			return nil, UserDTO{}, "", false
		}
	}
	var empty struct{}
	raw, ok := readOperationBody(w, r, 4096, &empty)
	if !ok {
		return nil, UserDTO{}, "", false
	}
	if !bytes.Equal(raw, []byte("{}")) {
		failure(w, 400, "invalid_operation_request")
		return nil, UserDTO{}, "", false
	}
	a, _, u, ok := s.authenticate(w, r)
	return a, u, key, ok
}

func (s *Server) recoverOperation(w http.ResponseWriter, r *http.Request) {
	a, u, key, ok := s.operationInitiator(w, r, true)
	if !ok {
		return
	}
	response, e := s.operationService().Recover(r.Context(), r.PathValue("grant"), key, a, u)
	if e != nil {
		operationFailure(w, e)
		return
	}
	respond(w, 200, response)
}

func (s *Server) revokeOperation(w http.ResponseWriter, r *http.Request) {
	a, u, _, ok := s.operationInitiator(w, r, false)
	if !ok {
		return
	}
	if e := s.operationService().Revoke(r.Context(), r.PathValue("grant"), a, u); e != nil {
		operationFailure(w, e)
		return
	}
	respond(w, 200, StatusDTO{Status: true})
}
