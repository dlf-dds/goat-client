// Userspace wireguard-go peer for the mobile smoke test. Listens on
// :51820, accepts a single peer (the mobile device's CPDevicePubkey),
// runs the standard wireguard-go handshake. Uses netstack tun — no OS
// interface created. Exits on SIGINT.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func main() {
	endpointPrivB64 := flag.String("endpoint-priv", "", "wg endpoint privkey (base64, 32 bytes)")
	clientPubB64 := flag.String("client-pub", "", "wg client pubkey (base64, 32 bytes)")
	port := flag.Int("port", 51820, "UDP listen port")
	flag.Parse()

	if *endpointPrivB64 == "" || *clientPubB64 == "" {
		log.Fatal("required: --endpoint-priv --client-pub")
	}
	priv, err := base64.StdEncoding.DecodeString(*endpointPrivB64)
	must(err)
	pub, err := base64.StdEncoding.DecodeString(*clientPubB64)
	must(err)

	tun, _, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("198.18.0.2")},
		nil, 1280)
	must(err)

	logger := device.NewLogger(device.LogLevelVerbose, "(smoke-endpoint) ")
	dev := device.NewDevice(tun, conn.NewDefaultBind(), logger)

	uapi := strings.NewReplacer("\\n", "\n").Replace(strings.Join([]string{
		"private_key=" + hex.EncodeToString(priv),
		fmt.Sprintf("listen_port=%d", *port),
		"replace_peers=true",
		"public_key=" + hex.EncodeToString(pub),
		"replace_allowed_ips=true",
		"allowed_ip=198.18.0.100/32",
	}, "\n") + "\n")
	must(dev.IpcSet(uapi))
	must(dev.Up())
	//nolint:forbidigo // operator-facing smoke tool; stdout is the interface
	fmt.Printf("smoke-endpoint listening on :%d, peer=%s\n", *port, *clientPubB64)

	// Poll the device's stats every 2s and emit handshake events.
	go func() {
		last := int64(0)
		for {
			time.Sleep(2 * time.Second)
			out, err := dev.IpcGet()
			if err != nil {
				continue
			}
			for _, line := range strings.Split(out, "\n") {
				if !strings.HasPrefix(line, "last_handshake_time_sec=") {
					continue
				}
				v := line[len("last_handshake_time_sec="):]
				var sec int64
				if _, err := fmt.Sscanf(v, "%d", &sec); err != nil {
					continue
				}
				if sec > 0 && sec != last {
					//nolint:forbidigo // operator-facing smoke tool; stdout is the interface
					fmt.Printf("HANDSHAKE %s\n", time.Unix(sec, 0).Format(time.RFC3339))
					last = sec
				}
			}
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	dev.Close()
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
