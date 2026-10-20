package bff

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAttendanceOneLiveAuthAndFullTargetGrant(t *testing.T) {
	key := strings.Repeat("attendance-independent", 2)
	var calls, owners atomic.Int64
	var revoked atomic.Bool
	user := map[string]any{"account_id": 12, "organization_id": 7, "division_id": 10, "role": "staff_admin", "organization_admin": false}
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/login" {
			json.NewEncoder(w).Encode(map[string]any{"status": true, "user": user, "organizations": []any{}, "access_token": "access", "refresh_token": "refresh", "expires_in": 900})
			return
		}
		if r.URL.Path != "/v1/auth/me" {
			t.Error("unexpected Auth RPC", r.URL.Path)
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"status": true, "active": true, "user": user, "organizations": []any{map[string]any{"id": 7, "divisions": []any{map[string]any{"id": 10}, map[string]any{"id": 20, "group_id": 33, "permissions": map[string]any{"attendance": map[string]any{"view": true, "edit": !revoked.Load()}}}}}, map[string]any{"id": 99, "divisions": []any{}}}})
	}))
	defer auth.Close()
	body := `{"expected_revision":1,"data":{"cells":[{"class_day_id":1,"account_id":8,"attendance_type_id":9}]}}`
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owners.Add(1)
		encoded := r.Header.Get("X-CPM-Gateway-Context")
		raw, _ := base64.RawURLEncoding.DecodeString(encoded)
		var claims gatewayClaims
		json.Unmarshal(raw, &claims)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(encoded))
		sig, _ := base64.RawURLEncoding.DecodeString(r.Header.Get("X-CPM-Gateway-Signature"))
		hash := sha256.Sum256([]byte(body))
		var identity struct {
			User struct {
				Division int64 `json:"division_id"`
			} `json:"user"`
			Organizations []struct {
				ID        int64 `json:"id"`
				Divisions []struct {
					ID          int64 `json:"id"`
					Group       int64 `json:"group_id"`
					Permissions map[string]struct {
						Edit bool `json:"edit"`
					} `json:"permissions"`
				} `json:"divisions"`
			} `json:"organizations"`
		}
		json.Unmarshal(claims.User, &identity)
		if claims.Audience != "attendance" || claims.URI != r.URL.RequestURI() || claims.Method != r.Method || claims.BodyHash != hex.EncodeToString(hash[:]) || !hmac.Equal(sig, mac.Sum(nil)) || identity.User.Division != 10 || len(identity.Organizations) != 1 || identity.Organizations[0].ID != 7 || len(identity.Organizations[0].Divisions) != 1 || identity.Organizations[0].Divisions[0].ID != 20 || identity.Organizations[0].Divisions[0].Group != 33 || identity.Organizations[0].Divisions[0].Permissions["attendance"].Edit == revoked.Load() {
			t.Error("live target grant lost or forged")
		}
		got, _ := io.ReadAll(r.Body)
		if string(got) != body {
			t.Error("body changed")
		}
		io.WriteString(w, `{"status":true,"data":{"revision":2,"value":{"cells":[]}}}`)
	}))
	defer owner.Close()
	s := New(Config{Auth: auth.URL, Attendance: owner.URL, AttendanceGatewayKey: key, Origin: "http://127.0.0.1:3001"})
	request := func(path, body, revision string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", revision)
		r.Header.Set("Idempotency-Key", "synthetic-key")
		r.Header.Set("X-CPM-Gateway-Context", "forged-browser-admin")
		r.Header.Set("X-CPM-Gateway-Signature", "forged")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	login := request("/bff/auth/login", `{}`, "", nil)
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body.String())
	}
	var identity struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(login.Body.Bytes(), &identity)
	for i := 0; i < 2; i++ {
		revoked.Store(i == 1)
		w := request("/bff/attendance/divisions/20/attendance-cells/batch", body, identity.Revision, login.Result().Cookies()[0])
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 || owners.Load() != 2 {
		t.Fatal("duplicate Auth/owner calls", calls.Load(), owners.Load())
	}
}

func TestAttendanceGatewayDistinctKeyConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("BFF_TESTS_CONTEXT_KEY", strings.Repeat("t", 32))
	t.Setenv("BFF_HOMEWORK_CONTEXT_KEY", strings.Repeat("h", 32))
	for _, test := range []struct {
		key   string
		valid bool
	}{{"", true}, {"short", false}, {strings.Repeat("t", 32), false}, {strings.Repeat("h", 32), false}, {strings.Repeat("a", 32), true}} {
		t.Setenv("BFF_ATTENDANCE_CONTEXT_KEY", test.key)
		_, _, err := LoadConfig()
		if (err == nil) != test.valid {
			t.Fatal("Attendance gateway configuration", err)
		}
	}
}
