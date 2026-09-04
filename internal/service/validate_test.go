package service

import (
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

func TestValidateItemFields(t *testing.T) {
	tests := []struct {
		name                  string
		typ, status, priority *string
		wantErr               bool
	}{
		{"all nil", nil, nil, nil, false},
		{"all empty", strp(""), strp(""), strp(""), false},
		{"valid", strp("bug"), strp("in_progress"), strp("urgent"), false},
		{"bad type", strp("epic"), nil, nil, true},
		{"bad status", nil, strp("doing"), nil, true},
		{"bad priority", nil, nil, strp("critical"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateItemFields(tt.typ, tt.status, tt.priority)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateItemFields() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateActivityKind(t *testing.T) {
	for _, k := range ValidActivityKinds {
		if err := ValidateActivityKind(&k); err != nil {
			t.Errorf("kind %q should be valid: %v", k, err)
		}
	}
	if err := ValidateActivityKind(nil); err != nil {
		t.Errorf("nil kind should be valid: %v", err)
	}
	if err := ValidateActivityKind(strp("musing")); err == nil {
		t.Error("kind 'musing' should be invalid")
	}
}

func TestValidateLinkKind(t *testing.T) {
	for _, k := range ValidLinkKinds {
		if err := ValidateLinkKind(&k); err != nil {
			t.Errorf("link kind %q should be valid: %v", k, err)
		}
	}
	if err := ValidateLinkKind(nil); err != nil {
		t.Errorf("nil link kind should be valid: %v", err)
	}
	if err := ValidateLinkKind(strp("depends_on")); err == nil {
		t.Error("link kind 'depends_on' should be invalid")
	}
}

func TestValidateConfidence(t *testing.T) {
	for _, c := range ValidConfidences {
		if err := ValidateConfidence(&c); err != nil {
			t.Errorf("confidence %q should be valid: %v", c, err)
		}
	}
	if err := ValidateConfidence(nil); err != nil {
		t.Errorf("nil confidence should be valid: %v", err)
	}
	if err := ValidateConfidence(strp("certain")); err == nil {
		t.Error("confidence 'certain' should be invalid")
	}
}

func TestValidateRefKind(t *testing.T) {
	for _, k := range ValidRefKinds {
		if err := ValidateRefKind(&k); err != nil {
			t.Errorf("ref kind %q should be valid: %v", k, err)
		}
	}
	if err := ValidateRefKind(strp("ticket")); err == nil {
		t.Error("ref kind 'ticket' should be invalid")
	}
}

func TestRejectedIsValidActivityKind(t *testing.T) {
	if err := ValidateActivityKind(strp("rejected")); err != nil {
		t.Errorf("'rejected' should be a valid activity kind: %v", err)
	}
}

func TestValidateProjectStatus(t *testing.T) {
	for _, ok := range []string{"active", "paused", "done", "archived"} {
		s := ok
		if err := ValidateProjectStatus(&s); err != nil {
			t.Fatalf("%s should be valid: %v", ok, err)
		}
	}
	bad := "bogus"
	if err := ValidateProjectStatus(&bad); err == nil {
		t.Fatal("bogus status should be rejected")
	}
	if err := ValidateProjectStatus(nil); err != nil {
		t.Fatalf("nil status should be skipped: %v", err)
	}
}

func i32p(v int32) *int32 { return &v }

func TestValidateContextImpactAcceptsContractCombinations(t *testing.T) {
	valid := []ContextImpactInput{
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "decision_changed", Evidence: "changed the plan"},
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "prevented_error", Evidence: "avoided a bad write", EstimatedMinutesDelta: i32p(10)},
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "duplicate_work_avoided", Evidence: "found prior work"},
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "reconstruction_saved", Evidence: "loaded prior state"},
		{SessionID: "s1", Project: "alpha", Effect: "neutral", Mechanism: "ignored", Evidence: "context was unrelated", EstimatedMinutesDelta: i32p(0)},
		{SessionID: "s1", Project: "alpha", Effect: "harmful", Mechanism: "stale_or_incorrect", Evidence: "context was wrong", EstimatedMinutesDelta: i32p(-10)},
	}
	for _, input := range valid {
		if err := ValidateContextImpact(input); err != nil {
			t.Errorf("valid input rejected: %v", err)
		}
	}
}

func TestValidateContextImpactRejectsInvalidInput(t *testing.T) {
	base := ContextImpactInput{
		SessionID: "s1", Project: "alpha", Effect: "helpful",
		Mechanism: "prevented_error", Evidence: "avoided a bad write",
	}
	tests := []struct {
		name   string
		mutate func(*ContextImpactInput)
	}{
		{"blank session", func(in *ContextImpactInput) { in.SessionID = "  " }},
		{"long session", func(in *ContextImpactInput) { in.SessionID = strings.Repeat("s", 201) }},
		{"blank project", func(in *ContextImpactInput) { in.Project = "" }},
		{"blank evidence", func(in *ContextImpactInput) { in.Evidence = "\n" }},
		{"long evidence", func(in *ContextImpactInput) { in.Evidence = strings.Repeat("e", 2001) }},
		{"invalid effect", func(in *ContextImpactInput) { in.Effect = "good" }},
		{"invalid mechanism", func(in *ContextImpactInput) { in.Mechanism = "remembered" }},
		{"invalid pair", func(in *ContextImpactInput) { in.Effect = "harmful" }},
		{"too many refs", func(in *ContextImpactInput) { in.ContextRefs = make([]string, 21) }},
		{"long ref", func(in *ContextImpactInput) { in.ContextRefs = []string{strings.Repeat("r", 201)} }},
		{"long idempotency key", func(in *ContextImpactInput) { key := strings.Repeat("k", 201); in.IdempotencyKey = &key }},
		{"delta above maximum", func(in *ContextImpactInput) { in.EstimatedMinutesDelta = i32p(1441) }},
		{"delta below minimum", func(in *ContextImpactInput) { in.EstimatedMinutesDelta = i32p(-1441) }},
		{"helpful negative delta", func(in *ContextImpactInput) { in.EstimatedMinutesDelta = i32p(-1) }},
		{"neutral positive delta", func(in *ContextImpactInput) {
			in.Effect = "neutral"
			in.Mechanism = "ignored"
			in.EstimatedMinutesDelta = i32p(1)
		}},
		{"harmful positive delta", func(in *ContextImpactInput) {
			in.Effect = "harmful"
			in.Mechanism = "stale_or_incorrect"
			in.EstimatedMinutesDelta = i32p(1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base
			tt.mutate(&input)
			if err := ValidateContextImpact(input); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}
