package authkey

import (
	"strings"
	"testing"
	"time"
)

var testPolicy = Policy{
	Name:         "testconfig",
	SecretPrefix: "test_",
	Domain:       "q.test.api-key.v1\x00",
}

func TestGenerateVerifyAndRevoke(t *testing.T) {
	masterKey := [32]byte{1, 2, 3}
	generated, err := Generate(masterKey, "  desktop  ", time.Unix(10, 0), testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Record.Alias != "desktop" || !strings.HasPrefix(generated.Secret, testPolicy.SecretPrefix) {
		t.Fatalf("generated = %#v", generated)
	}
	if !Verify(masterKey, generated.Record, generated.Secret, testPolicy) {
		t.Fatal("generated credential did not verify")
	}
	records, err := Revoke([]Record{generated.Record}, generated.Record.ID, time.Unix(20, 0), testPolicy.Name)
	if err != nil {
		t.Fatal(err)
	}
	if Verify(masterKey, records[0], generated.Secret, testPolicy) || ActiveCount(records) != 0 {
		t.Fatal("revoked credential remained active")
	}
}

func TestPolicySeparatesCredentialNamespaces(t *testing.T) {
	masterKey := [32]byte{4, 5, 6}
	generated, err := Generate(masterKey, "client", time.Unix(10, 0), testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	other := Policy{Name: "otherconfig", SecretPrefix: "other_", Domain: "q.other.api-key.v1\x00"}
	if Verify(masterKey, generated.Record, generated.Secret, other) {
		t.Fatal("credential verified under a different policy")
	}
	if _, _, ok := Parse(generated.Secret, other); ok {
		t.Fatal("credential parsed under a different prefix")
	}
}

func TestValidateRecordsRejectsMalformedHash(t *testing.T) {
	err := ValidateRecords([]Record{{
		ID: "id", Alias: "client", Hash: HashPrefix + "short", CreatedAt: time.Unix(10, 0),
	}}, testPolicy.Name)
	if err == nil || !strings.Contains(err.Error(), "invalid hash") {
		t.Fatalf("validation error = %v", err)
	}
}
