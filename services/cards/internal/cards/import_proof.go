package cards

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// ImportTarget identifies what the user approved, including the native
// create-by-name collision policy. It never invents a future theme ID.
type ImportTarget struct {
	Kind            string `json:"kind"`
	DirectionID     string `json:"direction_id"`
	ThemeID         string `json:"theme_id,omitempty"`
	Name            string `json:"name,omitempty"`
	CollisionPolicy string `json:"collision_policy,omitempty"`
}

type ImportProof struct {
	Scheme               string       `json:"scheme"`
	SHA256               string       `json:"sha256"`
	SourceSHA256         string       `json:"source_sha256"`
	OwnerPreviewRevision int64        `json:"owner_preview_revision"`
	OwnerRequestSHA256   string       `json:"owner_request_sha256"`
	Target               ImportTarget `json:"target"`
}

type importProofRow struct {
	Row      int    `json:"row"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Skip     bool   `json:"skip"`
}

type importProofSnapshot struct {
	Scheme         string           `json:"scheme"`
	AccountID      string           `json:"account_id"`
	OrganizationID string           `json:"organization_id"`
	SessionID      string           `json:"session_id"`
	Revision       string           `json:"revision"`
	SourceSHA256   string           `json:"source_sha256"`
	Target         ImportTarget     `json:"target"`
	Rows           []importProofRow `json:"rows"`
}

func lowerHex(value string, size int) bool {
	if len(value) != size || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Hash the stored owner snapshot, not display summaries or a client digest.
// Commit will compare this proof under the session lock before the first apply.
// A legacy preview without an authenticated source digest has no apply proof.
func importProof(a Actor, ss Session) *ImportProof {
	p := ss.Preview
	if ss.Kind != "import" || a.AccountID < 1 || a.OrganizationID < 1 ||
		ss.OrganizationID != a.OrganizationID || !lowerHex(ss.ID, 32) ||
		ss.Revision < 1 || p.DirectionID < 1 || ss.DirectionID != p.DirectionID ||
		!lowerHex(p.SourceSHA256, 64) {
		return nil
	}
	target := ImportTarget{Kind: "existing_theme", DirectionID: strconv.FormatInt(p.DirectionID, 10)}
	if p.CreateNew {
		if !validText(p.NewThemeName, 200) {
			return nil
		}
		target.Kind = "create_theme"
		target.Name = strings.TrimSpace(p.NewThemeName)
		target.CollisionPolicy = "reuse_existing"
	} else {
		if p.ThemeID < 1 {
			return nil
		}
		target.ThemeID = strconv.FormatInt(p.ThemeID, 10)
	}
	rows := make([]importProofRow, 0, len(p.Cards))
	for _, row := range p.Cards {
		rows = append(rows, importProofRow{row.Row, row.Question, row.Answer, row.Action == "skip"})
	}
	snapshot := importProofSnapshot{
		Scheme: "cards.xlsx.v1", AccountID: strconv.FormatInt(a.AccountID, 10),
		OrganizationID: strconv.FormatInt(a.OrganizationID, 10), SessionID: ss.ID,
		Revision: strconv.FormatInt(ss.Revision, 10), SourceSHA256: p.SourceSHA256,
		Target: target, Rows: rows,
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	digest := sha256.Sum256(raw)
	request, _ := json.Marshal(RevisionDTO{ExpectedRevision: ss.Revision})
	requestDigest := sha256.Sum256(request)
	return &ImportProof{snapshot.Scheme, hex.EncodeToString(digest[:]), p.SourceSHA256, ss.Revision, hex.EncodeToString(requestDigest[:]), target}
}
