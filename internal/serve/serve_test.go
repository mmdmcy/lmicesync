package serve

import (
	"testing"
	"time"
)

func TestBeaconSignAndVerify(t *testing.T) {
	now := time.Now()
	req := signBeacon("secret", roleRequest, beaconMsg{Device: "a", HTTPPort: 7370, Nonce: "00112233aabbccdd"}, now)
	if !verifyBeacon("secret", roleRequest, req, "", now) {
		t.Fatal("valid request rejected")
	}
	rep := signBeacon("secret", roleReply, beaconMsg{Device: "b", HTTPPort: 7371, Nonce: req.Nonce}, now)
	if !verifyBeacon("secret", roleReply, rep, req.Nonce, now) {
		t.Fatal("valid reply rejected")
	}
	if verifyBeacon("secret", roleReply, rep, "ffffffffffffffff", now) {
		t.Fatal("reply for another request's nonce accepted")
	}
	if verifyBeacon("secret", roleReply, req, req.Nonce, now) {
		t.Fatal("request reflected as a reply accepted")
	}
}

func TestBeaconWrongKeyDropped(t *testing.T) {
	now := time.Now()
	rep := signBeacon("secret", roleReply, beaconMsg{Device: "b", HTTPPort: 7370, Nonce: "00112233aabbccdd"}, now)
	if verifyBeacon("other", roleReply, rep, rep.Nonce, now) {
		t.Fatal("reply signed with another token accepted")
	}
	tampered := rep
	tampered.HTTPPort = 1
	if verifyBeacon("secret", roleReply, tampered, rep.Nonce, now) {
		t.Fatal("tampered reply accepted")
	}
	unsigned := beaconMsg{Device: "b", HTTPPort: 7370}
	if verifyBeacon("secret", roleReply, unsigned, "", now) || verifyBeacon("", roleReply, unsigned, "", now) {
		t.Fatal("unsigned (old-format) beacon accepted")
	}
}

func TestBeaconStaleDropped(t *testing.T) {
	now := time.Now()
	old := signBeacon("secret", roleReply, beaconMsg{Device: "b", HTTPPort: 7370, Nonce: "00112233aabbccdd"}, now.Add(-2*beaconWindow))
	if verifyBeacon("secret", roleReply, old, old.Nonce, now) {
		t.Fatal("stale reply accepted")
	}
	skewed := signBeacon("secret", roleReply, beaconMsg{Device: "b", HTTPPort: 7370, Nonce: "00112233aabbccdd"}, now.Add(-beaconWindow/2))
	if !verifyBeacon("secret", roleReply, skewed, skewed.Nonce, now) {
		t.Fatal("reply within the clock-skew window rejected")
	}
}

func TestSplitHTTPAddr(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"", "", 7370, true},
		{":7370", "", 7370, true},
		{"127.0.0.1:7370", "127.0.0.1", 7370, true},
		{"[::1]:8000", "::1", 8000, true},
		{"8000", "", 8000, true},
		{"127.0.0.1:", "127.0.0.1", 7370, true},
		{"127.0.0.1:x", "", 0, false},
		{"127.0.0.1:70000", "", 0, false},
	}
	for _, c := range cases {
		host, port, err := splitHTTPAddr(c.in)
		if (err == nil) != c.ok || host != c.host || port != c.port {
			t.Errorf("splitHTTPAddr(%q) = %q, %d, %v; want %q, %d, ok=%v", c.in, host, port, err, c.host, c.port, c.ok)
		}
	}
}
