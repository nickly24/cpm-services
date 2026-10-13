package cards

import "testing"

func TestCommandIdentityScopesAndPayload(t *testing.T) {
	key := "d89d1b67-8c64-41cd-9a5a-49df2d3610b4"
	a := Actor{AccountID: 8, OrganizationID: 11}
	body := ThemeDTO{Name: "Тема", DirectionID: 2}
	id, receipt, err := commandIdentity(a, key, "theme:create", body)
	if err != nil || len(id) != 32 {
		t.Fatal(id, err)
	}
	again, retry, err := commandIdentity(a, key, "theme:create", body)
	if err != nil || id != again || receipt != retry {
		t.Fatal("same command must retain its receipt")
	}
	otherBody := body
	otherBody.Name = "Другая тема"
	otherID, changed, _ := commandIdentity(a, key, "theme:create", otherBody)
	if otherID != id || changed == receipt {
		t.Fatal("changed input must conflict on the same key")
	}
	otherID, changed, _ = commandIdentity(a, key, "card:create:10", body)
	if otherID != id || changed == receipt {
		t.Fatal("same key on another operation must conflict")
	}
	for _, foreign := range []Actor{{AccountID: 9, OrganizationID: 11}, {AccountID: 8, OrganizationID: 12}} {
		otherID, _, _ = commandIdentity(foreign, key, "theme:create", body)
		if otherID == id {
			t.Fatal("accounts and organizations cannot share a receipt")
		}
	}
}

func TestCommandIdentityRejectsMalformedKeys(t *testing.T) {
	for _, key := range []string{"", "x", "contains space", "invalid/key", "кириллица", string(make([]byte, 129))} {
		if _, _, err := commandIdentity(Actor{}, key, "theme:create", ThemeDTO{}); err == nil {
			t.Fatalf("accepted malformed key %q", key)
		}
	}
}

func TestCommandKeyCompatibility(t *testing.T) {
	a := Actor{AccountID: 1, OrganizationID: 2}
	lower, _, _ := commandIdentity(a, "d89d1b67-8c64-41cd-9a5a-49df2d3610b4", "", nil)
	upper, _, _ := commandIdentity(a, "D89D1B67-8C64-41CD-9A5A-49DF2D3610B4", "", nil)
	if lower != upper {
		t.Fatal("UUID case changed existing receipt identity")
	}
	for _, key := range []string{"uploads_0123456789abcdef0123456789abcdef", "uploads_commit_0123456789abcdef0123456789abcdef", "ABC:1234"} {
		if _, _, e := commandIdentity(a, key, "", nil); e != nil {
			t.Fatal(e)
		}
	}
	lower, _, _ = commandIdentity(a, "opaque_key", "", nil)
	upper, _, _ = commandIdentity(a, "OPAQUE_KEY", "", nil)
	if lower == upper {
		t.Fatal("opaque keys must remain case sensitive")
	}
}
