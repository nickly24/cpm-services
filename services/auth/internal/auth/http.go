package auth

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed openapi.json
var openAPI []byte

type Server struct {
	Operations    *OperationService
	Repo          *Repository
	Tokens        *Tokens
	Config        Config
	passwordOnce  sync.Once
	passwordSlots chan struct{}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write(openAPI)
	})
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, StatusDTO{Status: true}) })
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.Tokens.JWKS()) })
	mux.HandleFunc("POST /internal/auth/password-hash", s.hashPassword)
	mux.HandleFunc("POST /v1/auth/login", s.login)
	mux.HandleFunc("POST /api/auth", s.login)
	mux.HandleFunc("POST /v1/auth/refresh", s.refresh)
	mux.HandleFunc("GET /v1/auth/me", s.me)
	mux.HandleFunc("POST /api/aun", s.me)
	mux.HandleFunc("POST /v1/auth/context", s.switchContext)
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("POST /v1/auth/introspect", s.me)
	if s.Config.Operations.Enabled {
		mux.HandleFunc("POST /internal/auth/operation-grants", s.issueOperation)
		mux.HandleFunc("POST /internal/auth/operation-grants/{grant}/authorize", s.authorizeOperation)
		mux.HandleFunc("POST /internal/auth/operation-grants/{grant}/recover", s.recoverOperation)
		mux.HandleFunc("POST /internal/auth/operation-grants/{grant}/revoke", s.revokeOperation)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/internal/auth/operation-grants") && !operationCanonicalHTTP(r) {
			failure(w, 400, "invalid_operation_request")
			return
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "path", r.URL.Path)
				failure(w, 500, "internal_error")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, ErrorDTO{Error: code})
}
func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if ct := strings.Split(r.Header.Get("Content-Type"), ";")[0]; strings.TrimSpace(ct) != "application/json" {
		failure(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		failure(w, 400, "invalid_request")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		failure(w, 400, "invalid_request")
		return false
	}
	return true
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.Repo.DB.PingContext(r.Context()) != nil {
		failure(w, 503, "dependency_unavailable")
		return
	}
	var count int
	if s.Repo.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_account_state").Scan(&count) != nil {
		failure(w, 503, "schema_not_ready")
		return
	}
	if s.Config.Operations.Enabled && s.Repo.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_operation_grants WHERE id=''").Scan(&count) != nil {
		failure(w, 503, "schema_not_ready")
		return
	}
	respond(w, 200, StatusDTO{Status: true})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input LoginDTO
	if !readJSON(w, r, &input) {
		return
	}
	input.Login = strings.TrimSpace(input.Login)
	if !utf8.ValidString(input.Login) || input.Login == "" || utf8.RuneCountInString(input.Login) > 50 || len(input.Password) == 0 || len(input.Password) > 1024 ||
		(input.OrganizationID != nil && *input.OrganizationID < 1) || (input.DivisionID != nil && (*input.DivisionID < 1 || input.OrganizationID == nil)) {
		failure(w, 400, "invalid_request")
		return
	}
	a, err := s.Repo.FindAccount(r.Context(), input.Login)
	if err != nil && !errors.Is(err, ErrCredentials) && !errors.Is(err, ErrForbidden) {
		failure(w, 503, "database_unavailable")
		return
	}
	if a == nil {
		if _, err := s.passwordWork(r.Context(), func() bool { DummyPasswordCheck(input.Password); return false }); err != nil {
			failure(w, 503, "request_cancelled")
			return
		}
		failure(w, 401, "invalid_credentials")
		return
	}
	matched, err := s.passwordWork(r.Context(), func() bool { return CheckPassword(a.Password, input.Password) })
	if err != nil {
		failure(w, 503, "request_cancelled")
		return
	}
	if !matched {
		failure(w, 401, "invalid_credentials")
		return
	}
	ctx, e := a.DefaultContext()
	if input.OrganizationID != nil {
		ctx = ContextDTO{OrganizationID: *input.OrganizationID, DivisionID: input.DivisionID}
		e = nil
		if ctx.DivisionID == nil && a.Role != "admin" {
			for _, o := range a.Organizations {
				if o.ID == ctx.OrganizationID && len(o.Divisions) > 0 {
					id := o.Divisions[0].ID
					ctx.DivisionID = &id
					break
				}
			}
		}
	}
	if e != nil {
		failure(w, 403, "context_forbidden")
		return
	}
	u, e := a.User(ctx)
	if e != nil {
		failure(w, 403, "context_forbidden")
		return
	}
	sid, e := RandomToken(16)
	if e != nil {
		failure(w, 500, "internal_error")
		return
	}
	refresh, e := RandomToken(32)
	if e != nil {
		failure(w, 500, "internal_error")
		return
	}
	session := Session{ContextVersion: 1, ID: sid, UserID: a.UserID, AuthVersion: a.AuthVersion, LegacySessionVersion: a.LegacySessionVersion, Context: ctx, ExpiresAt: time.Now().UTC().Add(s.Config.SessionTTL)}
	token, e := s.Tokens.Issue(*a, u, session)
	if e != nil {
		failure(w, 500, "internal_error")
		return
	}
	if e = s.Repo.NewSession(r.Context(), session, RefreshHash(refresh)); e != nil {
		failure(w, 503, "database_unavailable")
		return
	}
	s.tokenResponse(w, token, refresh, u, a.Organizations)
}

func (s *Server) tokenResponse(w http.ResponseWriter, token, refresh string, u UserDTO, organizations []Organization) {
	body := TokenResponseDTO{Status: true, Token: token, AccessToken: token, TokenType: "Bearer", ExpiresIn: int64(s.Config.AccessTTL.Seconds()), RefreshToken: refresh, User: u, Organizations: organizations}
	respond(w, 200, body)
}
func sameID(a, b *int64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*Account, Session, UserDTO, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		failure(w, 401, "invalid_token")
		return nil, Session{}, UserDTO{}, false
	}
	c, err := s.Tokens.Parse(parts[1])
	if err != nil {
		failure(w, 401, "invalid_token")
		return nil, Session{}, UserDTO{}, false
	}
	session, err := s.Repo.Session(r.Context(), c.SessionID)
	if err != nil {
		s.authError(w, err)
		return nil, Session{}, UserDTO{}, false
	}
	if session.ContextVersion != c.ContextVersion || session.UserID != c.AccountID || session.Context.OrganizationID != c.OrganizationID || !sameID(session.Context.DivisionID, c.DivisionID) {
		failure(w, 401, "invalid_token")
		return nil, Session{}, UserDTO{}, false
	}
	a, err := s.Repo.LoadAccount(r.Context(), c.AccountID)
	if err != nil {
		s.authError(w, err)
		return nil, Session{}, UserDTO{}, false
	}
	if a.AuthVersion != c.AuthVersion || a.LegacySessionVersion != c.SessionVersion || a.AuthVersion != session.AuthVersion || a.LegacySessionVersion != session.LegacySessionVersion || a.Role != c.Role || a.EntityID != c.UserDTO.ID {
		failure(w, 401, "invalid_token")
		return nil, Session{}, UserDTO{}, false
	}
	u, err := a.User(session.Context)
	if err != nil {
		s.authError(w, err)
		return nil, Session{}, UserDTO{}, false
	}
	return a, session, u, true
}
func (s *Server) authError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrToken) || errors.Is(err, ErrCredentials) || errors.Is(err, ErrForbidden) {
		failure(w, 401, "invalid_token")
		return
	}
	failure(w, 503, "database_unavailable")
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	a, _, u, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	respond(w, 200, IdentityResponseDTO{Status: true, Active: true, User: u, Organizations: a.Organizations, Role: u.Role, EntityID: u.ID, FullName: u.FullName, GroupID: u.GroupID, OrganizationID: u.OrganizationID, DivisionID: u.DivisionID, DivisionIDs: u.DivisionIDs, RoleID: u.RoleID, RoleName: u.RoleName, Permissions: u.Permissions})
}

func (s *Server) switchContext(w http.ResponseWriter, r *http.Request) {
	a, session, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input ContextDTO
	if !readJSON(w, r, &input) {
		return
	}
	if input.OrganizationID < 1 || (input.DivisionID != nil && *input.DivisionID < 1) {
		failure(w, 400, "invalid_request")
		return
	}
	u, err := a.User(input)
	if err != nil {
		failure(w, 403, "context_forbidden")
		return
	}
	if err = s.Repo.Switch(r.Context(), session, input); err != nil {
		s.authError(w, err)
		return
	}
	session.Context = input
	session.ContextVersion++
	token, err := s.Tokens.Issue(*a, u, session)
	if err != nil {
		failure(w, 500, "internal_error")
		return
	}
	s.tokenResponse(w, token, "", u, a.Organizations)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_, session, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if err := s.Repo.Revoke(r.Context(), session.ID); err != nil {
		s.authError(w, err)
		return
	}
	respond(w, 200, StatusDTO{Status: true})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var input RefreshDTO
	if !readJSON(w, r, &input) {
		return
	}
	if len(input.RefreshToken) != 64 {
		failure(w, 401, "invalid_token")
		return
	}
	hash := RefreshHash(input.RefreshToken)
	session, err := s.Repo.RefreshSession(r.Context(), hash)
	if err != nil {
		s.authError(w, err)
		return
	}
	a, err := s.Repo.LoadAccount(r.Context(), session.UserID)
	if err != nil {
		s.authError(w, err)
		return
	}
	next, err := RandomToken(32)
	if err != nil {
		failure(w, 500, "internal_error")
		return
	}
	var token string
	var u UserDTO
	err = s.Repo.RotateRefresh(r.Context(), session.ID, hash, RefreshHash(next), func(current Session) error {
		if a.AuthVersion != current.AuthVersion || a.LegacySessionVersion != current.LegacySessionVersion {
			return ErrToken
		}
		var e error
		u, e = a.User(current.Context)
		if e != nil {
			return e
		}
		token, e = s.Tokens.Issue(*a, u, current)
		return e
	})
	if err != nil {
		s.authError(w, err)
		return
	}
	s.tokenResponse(w, token, next, u, a.Organizations)
}
