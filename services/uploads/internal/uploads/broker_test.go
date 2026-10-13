package uploads

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testImportEnvelope() ImportEnvelope {
	return ImportEnvelope{MessageID: strings.Repeat("a", 32), MessageType: importExecuteRoute,
		SchemaVersion: 1, OccurredAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), Producer: "uploads",
		CorrelationID: strings.Repeat("b", 32), Payload: ImportCommand{
			Scope: Scope{OrganizationID: 2001, DivisionID: 301, ActorID: 7001}, JobID: strings.Repeat("b", 32),
			SessionID: strings.Repeat("c", 32), Generation: 1, Type: "cards.xlsx", Owner: "cards",
			OwnerSessionID: "cards-owner-session-123", SourceHash: strings.Repeat("d", 64),
			PreviewRevision: 3, OwnerRevision: 4,
		}}
}

func TestDecodeImportPinsScopeAndOwnerReferences(t *testing.T) {
	ev := testImportEnvelope()
	raw, e := json.Marshal(ev)
	if e != nil {
		t.Fatal(e)
	}
	got, e := decodeImport(raw)
	if e != nil {
		t.Fatal(e)
	}
	if got.Payload.Scope != ev.Payload.Scope || got.Payload.SourceHash != ev.Payload.SourceHash ||
		got.Payload.OwnerRevision != 4 || got.Payload.PreviewRevision != 3 || got.Payload.OwnerSessionID != ev.Payload.OwnerSessionID {
		t.Fatal("delivery lost scope or confirmed owner reference")
	}
	if strings.Contains(string(raw), "Bearer") || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "full_name") {
		t.Fatal("delivery must contain references only")
	}
}

func TestDecodeImportRejectsMalformedUnscopedAndCredentialEnvelopes(t *testing.T) {
	base := testImportEnvelope()
	cases := map[string]func(*ImportEnvelope){
		"organization_missing":      func(e *ImportEnvelope) { e.Payload.OrganizationID = 0 },
		"division_missing":          func(e *ImportEnvelope) { e.Payload.DivisionID = 0 },
		"actor_missing":             func(e *ImportEnvelope) { e.Payload.ActorID = 0 },
		"scope_negative":            func(e *ImportEnvelope) { e.Payload.OrganizationID = -1 },
		"owner_mismatch":            func(e *ImportEnvelope) { e.Payload.Owner = "core" },
		"source_changed":            func(e *ImportEnvelope) { e.Payload.SourceHash = "invalid" },
		"revision_missing":          func(e *ImportEnvelope) { e.Payload.OwnerRevision = 0 },
		"generation_missing":        func(e *ImportEnvelope) { e.Payload.Generation = 0 },
		"correlation_changed":       func(e *ImportEnvelope) { e.CorrelationID = strings.Repeat("e", 32) },
		"unsupported_kind":          func(e *ImportEnvelope) { e.Payload.Type = "homework.pdf"; e.Payload.Owner = "homework" },
		"producer_changed":          func(e *ImportEnvelope) { e.Producer = "browser" },
		"schema_changed":            func(e *ImportEnvelope) { e.SchemaVersion = 2 },
		"job_id_invalid":            func(e *ImportEnvelope) { e.Payload.JobID = "../other" },
		"owner_reference_oversized": func(e *ImportEnvelope) { e.Payload.OwnerSessionID = strings.Repeat("x", 129) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			ev := base
			change(&ev)
			raw, _ := json.Marshal(ev)
			if _, e := decodeImport(raw); e == nil {
				t.Fatal("accepted invalid administrative delivery")
			}
		})
	}
	raw, _ := json.Marshal(base)
	for name, b := range map[string][]byte{
		"authorization":   []byte(strings.Replace(string(raw), `"payload":{`, `"payload":{"authorization":"Bearer synthetic",`, 1)),
		"source_rows":     []byte(strings.Replace(string(raw), `"payload":{`, `"payload":{"rows":[{"password":"synthetic"}],`, 1)),
		"trailing_object": append(append([]byte{}, raw...), []byte(` {}`)...),
		"oversized":       []byte(strings.Repeat(" ", 8193)), "empty": []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := decodeImport(b); e == nil {
				t.Fatal("accepted unsafe transport payload")
			}
		})
	}
}

func TestDeliveryRetryDelayIsBounded(t *testing.T) {
	for _, n := range []int{-10, 0, 1, 2, 6, 1000} {
		d := deliveryBackoff(n)
		if d < 2*time.Second || d > 64*time.Second {
			t.Fatalf("unbounded retry delay for attempt %d", n)
		}
	}
}

func TestRabbitURLAllowsOnlyGuardedLocalBroker(t *testing.T) {
	for _, raw := range []string{"amqp://user:password@127.0.0.1:5672/cpm_dev", "amqp://user:password@rabbitmq:5672/cpm_dev"} {
		if e := validateRabbitURL(raw); e != nil {
			t.Fatal("guard rejected local broker")
		}
	}
	for _, raw := range []string{"", "amqp://user:password@production.example:5672/cpm_dev", "amqp://user:password@127.0.0.1:5673/cpm_dev", "amqp://user@127.0.0.1:5672/cpm_dev", "http://user:password@127.0.0.1:5672/cpm_dev", "amqp://user:password@127.0.0.1:5672/cpm_dev?heartbeat=1", "amqp://user:password@127.0.0.1:5672/production", "amqp://user:password@127.0.0.1:5672/%2F"} {
		if e := validateRabbitURL(raw); e == nil {
			t.Fatal("guard accepted unsupported broker")
		}
	}
}
