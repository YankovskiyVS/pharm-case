package app

import (
	"bytes"
	"psitech/internal/mailer"
	"testing"
)

func TestInvitationPayloadEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{23}, 32)
	raw := "one-time-invitation-bearer-token"
	id := "00000000-0000-4000-8000-000000000001"
	encrypted, err := sealInvitation(key, id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(raw)) {
		t.Fatal("invitation token stored plaintext")
	}
	opened, err := openInvitation(key, id, encrypted)
	if err != nil || opened != raw {
		t.Fatal("invitation roundtrip failed")
	}
	if _, err = openInvitation(key, "different-invitation", encrypted); err == nil {
		t.Fatal("ciphertext not bound to invitation")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err = openInvitation(key, id, encrypted); err == nil {
		t.Fatal("tampered payload accepted")
	}
	if _, err = openInvitation(key, id, []byte{1}); err == nil {
		t.Fatal("short payload accepted")
	}
}
func TestEmailConfiguration(t *testing.T) {
	sender, err := mailer.New(mailer.Config{From: "notice@psitech.local", Credentials: mailer.Credentials{ClientID: "test", ClientSecret: "secret", RefreshToken: "refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	good := EmailConfig{Enabled: true, PublicURL: "http://localhost:8080", EncryptionKey: bytes.Repeat([]byte{1}, 32), Gmail: sender}
	if err := ValidateEmailConfig(good); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://remote.example", "https://user:password@example.org", "https://example.org/path", "https://example.org?token=bad", "javascript:evil"} {
		c := good
		c.PublicURL = origin
		if ValidateEmailConfig(c) == nil {
			t.Fatalf("unsafe origin accepted %s", origin)
		}
	}
	invalid := good
	invalid.EncryptionKey = []byte{1}
	if ValidateEmailConfig(invalid) == nil {
		t.Fatal("short encryption key accepted")
	}
	if err := ValidateEmailConfig(EmailConfig{}); err != nil {
		t.Fatal("disabled channel requires secrets")
	}
}
