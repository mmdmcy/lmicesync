// Package serve implements the LAN agent: a UDP discovery beacon and a small
// HTTP server that serves git bundles of auto/* refs to approved, token
// authenticated peers. No public network traffic is ever generated.
package serve

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmdmcy/lmicesync/internal/config"
	"github.com/mmdmcy/lmicesync/internal/scan"
)

const (
	defaultHTTPPort = 7370
	// beaconWindow bounds clock skew between peers: a beacon whose ts is
	// further than this from the receiver's clock is dropped as stale.
	beaconWindow = 60 * time.Second
)

// Peer describes a discovered device.
type Peer struct {
	Device     string `json:"device"`
	HTTPHost   string `json:"http_host"`
	HTTPPort   int    `json:"http_port"`
	Discovered string `json:"discovered,omitempty"`
}

// beaconMsg is both the discovery request (broadcast) and the reply
// (unicast). Both are authenticated with the shared token: MAC is
// hex(HMAC-SHA256(token, role|device|http_port|ts|nonce)). A reply echoes the
// request's nonce, so a reply can only answer the request it was made for;
// anything unsigned, wrongly signed, stale or for another nonce is ignored.
// The token therefore only ever goes (over HTTP) to a host that proved it
// already holds it.
type beaconMsg struct {
	Device   string `json:"device"`
	HTTPPort int    `json:"http_port"`
	TS       int64  `json:"ts,omitempty"`    // unix seconds
	Nonce    string `json:"nonce,omitempty"` // 16 hex chars
	MAC      string `json:"mac,omitempty"`   // 64 hex chars
}

const (
	roleRequest = "lmicesync.v1/request"
	roleReply   = "lmicesync.v1/reply"
)

func beaconMAC(token, role string, m beaconMsg) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	fmt.Fprintf(mac, "%s\n%s\n%d\n%d\n%s", role, m.Device, m.HTTPPort, m.TS, m.Nonce)
	return mac.Sum(nil)
}

func newNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// signBeacon stamps m with now and the MAC for role. Nonce must be set.
func signBeacon(token, role string, m beaconMsg, now time.Time) beaconMsg {
	m.TS = now.Unix()
	m.MAC = hex.EncodeToString(beaconMAC(token, role, m))
	return m
}

// verifyBeacon reports whether m is well formed, fresh and signed with token
// for role. With wantNonce set, m must carry exactly that nonce.
func verifyBeacon(token, role string, m beaconMsg, wantNonce string, now time.Time) bool {
	if token == "" || m.Device == "" || m.Nonce == "" || (wantNonce != "" && m.Nonce != wantNonce) {
		return false
	}
	got, err := hex.DecodeString(m.MAC)
	if err != nil || !hmac.Equal(got, beaconMAC(token, role, m)) {
		return false
	}
	age := now.Sub(time.Unix(m.TS, 0))
	return age <= beaconWindow && age >= -beaconWindow
}

func ownAddr() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return strings.Split(conn.LocalAddr().String(), ":")[0]
}

// splitHTTPAddr parses http_addr ("host:port", ":port" or "port") into the
// host to bind (empty = all interfaces) and the port.
func splitHTTPAddr(addr string) (host string, port int, err error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", defaultHTTPPort, nil
	}
	portStr := addr
	if strings.Contains(addr, ":") {
		if host, portStr, err = net.SplitHostPort(addr); err != nil {
			return "", 0, fmt.Errorf("http_addr %q: %w", addr, err)
		}
	}
	if portStr == "" {
		return host, defaultHTTPPort, nil
	}
	port, err = strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("http_addr %q: invalid port %q", addr, portStr)
	}
	return host, port, nil
}

// httpPortFor extracts the configured HTTP port from http_addr.
func httpPortFor(cfg *config.Config) int {
	_, port, err := splitHTTPAddr(cfg.HTTPAddr)
	if err != nil {
		return defaultHTTPPort
	}
	return port
}

// Run starts the agent: beacon responder plus HTTP server. Blocks.
func Run(cfg *config.Config) error {
	if cfg.Token == "" {
		return fmt.Errorf("serve requires a token; set LMICESYNC_TOKEN or add token to the config")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	repos := scan.FindRepos(cfg.Roots)
	byName := map[string]string{}
	for _, r := range repos {
		byName[filepath.Base(r)] = r
	}

	host, port, err := splitHTTPAddr(cfg.HTTPAddr)
	if err != nil {
		return err
	}
	shown := host
	if shown == "" {
		shown = ownAddr()
	}

	go beaconResponder(cfg, port)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v1/sync/", func(w http.ResponseWriter, req *http.Request) {
		if !authorized(req, cfg) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !peerApproved(req.URL.Query().Get("device"), cfg) {
			http.Error(w, "device not approved", http.StatusForbidden)
			return
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/sync/")
		dir, ok := byName[name]
		if !ok {
			http.Error(w, "unknown repository", http.StatusNotFound)
			return
		}
		bundleRepo(w, req, dir)
	})
	fmt.Printf("lmicesync agent on http://%s (device %s)\n", net.JoinHostPort(shown, strconv.Itoa(port)), cfg.Device)
	// Bind exactly what http_addr says: "127.0.0.1:7370" stays on loopback.
	return http.ListenAndServe(net.JoinHostPort(host, strconv.Itoa(port)), mux)
}

func authorized(r *http.Request, cfg *config.Config) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(tok), []byte(cfg.Token)) == 1
}

// peerApproved reports whether the requesting device may pull bundles. With
// require_approval off, every device is approved. The device name is the
// caller's own claim (?device=): every caller already holds the shared
// token, so this is a convenience allowlist (serve only to fleet members I
// listed), not access control against a token holder.
func peerApproved(device string, cfg *config.Config) bool {
	if !cfg.RequireApproval {
		return true
	}
	if device == "" {
		return false
	}
	data, err := os.ReadFile(cfg.ApprovalFile)
	if err != nil {
		return false
	}
	var list []string
	if json.Unmarshal(data, &list) != nil {
		return false
	}
	for _, d := range list {
		if d == device {
			return true
		}
	}
	return false
}

// bundleRepo streams a git bundle containing all auto/* refs of the repo.
func bundleRepo(w http.ResponseWriter, req *http.Request, dir string) {
	bundle, err := os.CreateTemp("", "lmicesync-bundle-*")
	if err != nil {
		http.Error(w, "bundle failed", http.StatusInternalServerError)
		return
	}
	defer os.Remove(bundle.Name())
	defer bundle.Close()

	refsOut, err := exec.Command("git", "-C", dir, "for-each-ref",
		"--format=%(refname)", "refs/heads/auto/").Output()
	if err != nil || strings.TrimSpace(string(refsOut)) == "" {
		http.Error(w, "no auto refs", http.StatusNotFound)
		return
	}
	refs := strings.Fields(string(refsOut))
	args := append([]string{"-C", dir, "bundle", "create", bundle.Name()}, refs...)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("bundle failed: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-bundle")
	http.ServeFile(w, req, bundle.Name())
}

func beaconResponder(cfg *config.Config, httpPort int) {
	conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", cfg.BeaconPort))
	if err != nil {
		fmt.Printf("beacon listen failed: %v\n", err)
		return
	}
	defer conn.Close()
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var msg beaconMsg
		if json.Unmarshal(buf[:n], &msg) != nil || msg.Device == cfg.Device {
			continue
		}
		if !verifyBeacon(cfg.Token, roleRequest, msg, "", time.Now()) {
			continue // unsigned, wrong token or stale: never answer
		}
		reply, _ := json.Marshal(signBeacon(cfg.Token, roleReply,
			beaconMsg{Device: cfg.Device, HTTPPort: httpPort, Nonce: msg.Nonce}, time.Now()))
		_, _ = conn.WriteTo(reply, addr)
	}
}

// Discover broadcasts a signed beacon and collects peer replies for the given
// wait. Only replies signed with the shared token for this request's nonce
// are returned.
func Discover(cfg *config.Config, wait time.Duration) ([]Peer, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("discovery requires a token; set LMICESYNC_TOKEN or add token to the config")
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	msg, _ := json.Marshal(signBeacon(cfg.Token, roleRequest,
		beaconMsg{Device: cfg.Device, HTTPPort: httpPortFor(cfg), Nonce: nonce}, time.Now()))
	for _, ip := range []net.IP{net.IPv4(255, 255, 255, 255), net.IPv4(127, 0, 0, 1)} {
		if _, err := conn.WriteTo(msg, &net.UDPAddr{IP: ip, Port: cfg.BeaconPort}); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(wait)
	seen := map[string]bool{}
	var peers []Peer
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		var reply beaconMsg
		if json.Unmarshal(buf[:n], &reply) != nil || reply.Device == cfg.Device ||
			!verifyBeacon(cfg.Token, roleReply, reply, nonce, time.Now()) {
			continue
		}
		host := strings.Split(from.String(), ":")[0]
		port := reply.HTTPPort
		if port == 0 {
			port = defaultHTTPPort
		}
		key := reply.Device + "@" + host
		if seen[key] {
			continue
		}
		seen[key] = true
		peers = append(peers, Peer{Device: reply.Device, HTTPHost: host, HTTPPort: port})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Device < peers[j].Device })
	return peers, nil
}

// PrintPeers implements `lmicesync peers`.
func PrintPeers(cfg *config.Config) error {
	peers, err := Discover(cfg, 3*time.Second)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Println("no peers discovered")
		return nil
	}
	for _, p := range peers {
		fmt.Printf("%s %s:%d\n", p.Device, p.HTTPHost, p.HTTPPort)
	}
	return nil
}
