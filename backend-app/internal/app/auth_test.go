package app

import (
	"testing"
	"time"
)

func TestTOTPStandardVector(t *testing.T) {
	// RFC 6238 SHA-1 vector at 59 seconds, reduced to six digits.
	if got := TOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0)); got != "287082" {
		t.Fatalf("TOTP %s", got)
	}
	if TOTP("?", time.Now()) != "" {
		t.Fatal("invalid base32 accepted")
	}
	if validTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "123") {
		t.Fatal("invalid code length accepted")
	}
}
func TestUUIDValidation(t *testing.T) {
	for _, id := range []string{"", "not-a-uuid", "00000000_0000-0000-0000-000000000000", "zzzzzzzz-0000-0000-0000-000000000000"} {
		if uuid(id) {
			t.Fatal(id)
		}
	}
	if !uuid("00000000-0000-0000-0000-000000000000") {
		t.Fatal("valid UUID rejected")
	}
}
