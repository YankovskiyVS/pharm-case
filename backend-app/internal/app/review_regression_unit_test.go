package app

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestReviewRegressionTOTPRejectsUnconfiguredSecrets(t *testing.T) {
	for _, secret := range []string{"", "AA", "MZXW6", "not valid base32!"} {
		t.Run(secret, func(t *testing.T) {
			if got := TOTP(secret, time.Unix(59, 0)); got != "" {
				t.Fatalf("unconfigured/weak secret produced code %q", got)
			}
			if validTOTP(secret, "000000") {
				t.Fatal("unconfigured/weak secret passed doctor MFA")
			}
		})
	}
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if !validTOTP(secret, TOTP(secret, time.Now())) {
		t.Fatal("configured doctor TOTP should remain valid")
	}
}

func TestReviewRegressionOnlyTrustedProxyCanSetClientAddress(t *testing.T) {
	cases := []struct {
		name, remote, forwarded string
		trusted                 []string
		want                    string
	}{
		{"direct spoof ignored", "198.51.100.7:4321", "203.0.113.8", nil, "198.51.100.7"},
		{"trusted frontend", "172.20.0.3:4321", "203.0.113.8", []string{"172.20.0.0/16"}, "203.0.113.8"},
		{"other proxy ignored", "172.21.0.3:4321", "203.0.113.8", []string{"172.20.0.0/16"}, "172.21.0.3"},
		{"invalid forwarded ignored", "172.20.0.3:4321", "not an IP", []string{"172.20.0.0/16"}, "172.20.0.3"},
		{"IPv6 trusted", "[fd00::3]:4321", "2001:db8::1", []string{"fd00::/64"}, "2001:db8::1"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{}
			for _, prefix := range tt.trusted {
				config.TrustedProxyCIDRs = append(config.TrustedProxyCIDRs, netip.MustParsePrefix(prefix))
			}
			server := New(nil, config)
			request := httptest.NewRequest("POST", "http://localhost:8080/api/v1/auth/login", nil)
			request.RemoteAddr = tt.remote
			request.Header.Set("X-Real-IP", tt.forwarded)
			if got := server.clientIP(request); got != tt.want {
				t.Fatalf("client IP = %s; want %s", got, tt.want)
			}
		})
	}
}
