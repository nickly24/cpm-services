package cards

import (
	"strings"
	"testing"
)

func proofFixture() (Actor, Session) {
	return Actor{AccountID: 7, OrganizationID: 11}, Session{
		ID: "0123456789abcdef0123456789abcdef", Kind: "import", OrganizationID: 11,
		DirectionID: 13, Revision: 2, State: "preview",
		Preview: Preview{DirectionID: 13, ThemeID: 17, SourceSHA256: strings.Repeat("a", 64),
			Cards: []ImportRow{{Row: 2, Question: "Q", Answer: "A\nB", Action: "create"},
				{Row: 3, Question: "Skipped", Action: "skip"}}},
	}
}

func TestImportProofGoldenAndDisplayIndependence(t *testing.T) {
	a, ss := proofFixture()
	p := importProof(a, ss)
	if p == nil || p.SHA256 != "6ae93981f05b34e30fc793dcda38a56f671a1521d02cbc3362c4df0ec3b0fc64" {
		t.Fatalf("canonical snapshot changed: %+v", p)
	}
	ss.Preview.Cards[0].Action = "warning"
	ss.Preview.Cards[0].Warnings = []string{"duplicate_in_section"}
	ss.Preview.Summary.Warning = 1
	ss.Preview.SourceFilename = "renamed.xlsx"
	ss.State = "completed"
	if got := importProof(a, ss); got == nil || *got != *p {
		t.Fatal("display/replay state altered confirmed input")
	}
}

func TestImportProofPinsEveryBusinessInput(t *testing.T) {
	changes := map[string]func(*Actor, *Session){
		"creator":      func(a *Actor, s *Session) { a.AccountID++ },
		"organization": func(a *Actor, s *Session) { a.OrganizationID++; s.OrganizationID++ },
		"session":      func(a *Actor, s *Session) { s.ID = strings.Repeat("b", 32) },
		"revision":     func(a *Actor, s *Session) { s.Revision++ },
		"source":       func(a *Actor, s *Session) { s.Preview.SourceSHA256 = strings.Repeat("b", 64) },
		"direction":    func(a *Actor, s *Session) { s.DirectionID++; s.Preview.DirectionID++ },
		"theme":        func(a *Actor, s *Session) { s.Preview.ThemeID++ },
		"question":     func(a *Actor, s *Session) { s.Preview.Cards[0].Question += " changed" },
		"answer":       func(a *Actor, s *Session) { s.Preview.Cards[0].Answer = "A B" },
		"skip":         func(a *Actor, s *Session) { s.Preview.Cards[0].Action = "skip" },
		"row":          func(a *Actor, s *Session) { s.Preview.Cards[0].Row = 8 },
		"row order": func(a *Actor, s *Session) {
			s.Preview.Cards[0], s.Preview.Cards[1] = s.Preview.Cards[1], s.Preview.Cards[0]
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			a, ss := proofFixture()
			before := importProof(a, ss)
			change(&a, &ss)
			after := importProof(a, ss)
			if after == nil || after.SHA256 == before.SHA256 {
				t.Fatal("input change was not pinned")
			}
		})
	}
}

func TestImportProofRejectsUnboundAndTransformSources(t *testing.T) {
	for _, change := range []func(*Actor, *Session){
		func(a *Actor, s *Session) { a.AccountID = 0 },
		func(a *Actor, s *Session) { a.OrganizationID++ },
		func(a *Actor, s *Session) { s.Kind = "transform" },
		func(a *Actor, s *Session) { s.Preview.SourceSHA256 = "" },
		func(a *Actor, s *Session) { s.Preview.SourceSHA256 = strings.Repeat("A", 64) },
		func(a *Actor, s *Session) { s.Preview.ThemeID = 0 },
		func(a *Actor, s *Session) { s.Revision = 0 },
		func(a *Actor, s *Session) { s.DirectionID++ },
	} {
		a, ss := proofFixture()
		change(&a, &ss)
		if importProof(a, ss) != nil {
			t.Fatal("unbound source received an apply proof")
		}
	}
}

func TestImportProofCreateTargetHasExplicitCollisionPolicy(t *testing.T) {
	a, ss := proofFixture()
	ss.Preview.CreateNew = true
	ss.Preview.NewThemeName = " New theme "
	ss.Preview.ThemeID = 0
	p := importProof(a, ss)
	if p == nil || p.Target.Kind != "create_theme" || p.Target.ThemeID != "" ||
		p.Target.Name != "New theme" || p.Target.CollisionPolicy != "reuse_existing" {
		t.Fatalf("ambiguous create target: %+v", p)
	}
	// Native create-by-name deliberately reuses an org/direction name collision.
	ss.Preview.ThemeID = 99
	ss.Preview.Collision = true
	if next := importProof(a, ss); next == nil || next.SHA256 != p.SHA256 {
		t.Fatal("allowed name collision invented a different confirmed target")
	}
	ss.Preview.NewThemeName = "Other theme"
	if next := importProof(a, ss); next == nil || next.SHA256 == p.SHA256 {
		t.Fatal("theme name is not pinned")
	}
}
