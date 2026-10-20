package attendance

import (
	"context"
	"strconv"
	"strings"
)

// Numeric QR payloads are student profile IDs, never account IDs.
// Keep signed day-specific tokens compatible during the rollout.
func (s *Server) scanAccount(ctx context.Context, st *State, a Identity, division, day int64, raw string) (int64, error) {
	token := strings.TrimSpace(raw)
	if strings.HasPrefix(token, "attendance-qr-v1.") {
		claims, err := s.verifyQR(token)
		if err != nil {
			return 0, err
		}
		if claims.OrganizationID != a.User.OrganizationID || claims.DivisionID != division || claims.DayID != day {
			return 0, bad("qr_scope_mismatch")
		}
		return claims.AccountID, nil
	}
	v, ok := st.Days[day]
	if !ok || v.DivisionID != division {
		return 0, missing
	}
	profile, err := strconv.ParseInt(token, 10, 64)
	if err != nil || profile < 1 || strconv.FormatInt(profile, 10) != token {
		return 0, bad("invalid_student_qr")
	}
	member, err := s.Peers.StudentProfile(ctx, a.User.OrganizationID, division, profile)
	if err != nil {
		return 0, err
	}
	return member.AccountID, nil
}
