package service

import "testing"

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
