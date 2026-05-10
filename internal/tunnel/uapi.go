package tunnel

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	wgdevice "golang.zx2c4.com/wireguard/device"
)

// buildUAPI renders cfg to wireguard-go's UAPI text format. UAPI keys are
// documented at https://www.wireguard.com/xplatform/. The format is line-
// oriented `key=value` pairs; IpcSet accepts either a trailing-newline or
// double-newline terminated block. Shared between the desktop tunnel
// (tunnel_desktop.go) and the mobile entry point (runmobile.go).
func buildUAPI(cfg Config) (string, error) {
	if len(cfg.PrivateKey) != 32 {
		return "", errors.New("private key not 32 bytes")
	}
	if len(cfg.Peer.PublicKey) != 32 {
		return "", errors.New("peer public key not 32 bytes")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", hex.EncodeToString(cfg.PrivateKey))
	if cfg.ListenPort > 0 {
		fmt.Fprintf(&sb, "listen_port=%d\n", cfg.ListenPort)
	}
	sb.WriteString("replace_peers=true\n")
	fmt.Fprintf(&sb, "public_key=%s\n", hex.EncodeToString(cfg.Peer.PublicKey))
	if cfg.Peer.Endpoint != "" {
		host, port, err := net.SplitHostPort(cfg.Peer.Endpoint)
		if err != nil {
			return "", fmt.Errorf("split endpoint %q: %w", cfg.Peer.Endpoint, err)
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return "", fmt.Errorf("resolve endpoint host %q: %w", host, err)
		}
		fmt.Fprintf(&sb, "endpoint=%s\n", net.JoinHostPort(ips[0].String(), port))
	}
	if cfg.Peer.PersistentKeepalive > 0 {
		fmt.Fprintf(&sb, "persistent_keepalive_interval=%d\n", int(cfg.Peer.PersistentKeepalive.Seconds()))
	}
	sb.WriteString("replace_allowed_ips=true\n")
	for _, p := range cfg.Peer.AllowedIPs {
		fmt.Fprintf(&sb, "allowed_ip=%s\n", p.String())
	}
	return sb.String(), nil
}

func splitKV(line string) (string, string, bool) {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", false
	}
	return line[:eq], line[eq+1:], true
}

// readUAPIStats issues an IpcGet against dev and parses the response into
// Stats. UAPI emits `tx_bytes`, `rx_bytes`, `last_handshake_time_sec` (and
// _nsec) per peer. Single-peer model means we just take the last
// occurrence; multi-peer would need accumulation.
func readUAPIStats(dev *wgdevice.Device) (Stats, error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer pw.Close()
		errCh <- dev.IpcGetOperation(pw)
	}()
	var stats Stats
	scanner := newLineScanner(pr)
	for scanner.Scan() {
		k, v, ok := splitKV(scanner.Text())
		if !ok {
			continue
		}
		switch k {
		case "tx_bytes":
			stats.BytesOut, _ = strconv.ParseUint(v, 10, 64)
		case "rx_bytes":
			stats.BytesIn, _ = strconv.ParseUint(v, 10, 64)
		case "last_handshake_time_sec":
			s, _ := strconv.ParseInt(v, 10, 64)
			if s > 0 {
				stats.LastHandshake = time.Unix(s, 0)
			}
		}
	}
	if err := <-errCh; err != nil {
		return stats, fmt.Errorf("ipc get: %w", err)
	}
	return stats, nil
}
