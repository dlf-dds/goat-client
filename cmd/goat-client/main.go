// Package main is the goat-client desktop GUI + CLI binary.
//
// Two roles:
//
//   - Default (no flags, no subcommand): runs as the systray parent.
//     Holds the tray icon, polls goat-clientd for status, exposes
//     Connect/Disconnect plus an "Open window..." menu item.
//   - --window: runs as the Fyne window child, spawned by the parent on
//     demand. Carries the bundle-import dialog, status pane, and
//     diagnostics tab.
//
// Subcommands (v0.2): query / update daemon state without launching the
// GUI. Useful for ops scripts and headless boxes that still have the
// goat-client binary on PATH.
//
//	goat-client getmode             — print the active mode + exit
//	goat-client setmode <mode>      — switch mode + exit (re-uses daemon IPC)
//	goat-client connect             — bring up the active mode's subsystems
//	goat-client disconnect          — tear down the active mode's subsystems
//	goat-client status              — print the current status snapshot + exit
//
// connect / disconnect / status make the daemon driveable from a shell
// without the Fyne GUI — the load-bearing path for headless installs
// (Orin sites, server installs) and operator scripts. The IPC server
// already accepts MethodConnect / MethodDisconnect / MethodGetStatus
// (the GUI uses them); the subcommands are thin handlers around the
// existing internal/ipc client.
//
// Argument-order tolerance: subcommand may appear before OR after the
// global --daemon-addr flag. Both of these work:
//
//	goat-client status --daemon-addr unix:/tmp/foo.sock
//	goat-client --daemon-addr unix:/tmp/foo.sock status
//
// The first form's subcommand-then-flags parse happens in the
// subcommand handler's own flag.NewFlagSet; the second form's
// flag-then-subcommand parse uses the top-level flag.Parse which stops
// at the first positional. The global --daemon-addr value is threaded
// into the subcommand handler as its default; the handler's local
// flag.NewFlagSet re-parses it if a second instance appears in the
// trailing args (last-write-wins).
//
// The GUI split is the netbird upstream pattern and exists because
// fyne.io/systray's NSStatusItem and Fyne's NSApplication both want the
// macOS main thread; running them in separate processes is the safe path.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
	"github.com/dlf-dds/goat-client/internal/ui"
)

// knownSubcommands is the set of recognised positional verbs. Kept as a
// package-level so tests can assert "every subcommand listed in
// printUsage has a dispatcher entry."
var knownSubcommands = map[string]bool{
	"getmode":    true,
	"setmode":    true,
	"connect":    true,
	"disconnect": true,
	"status":     true,
	"send":       true,
	"help":       true,
}

func main() {
	// Top-level flag parse. Global flags consumed before the first
	// positional (which is the subcommand, if any). flag.Parse stops at
	// the first non-flag argument, so both arg orders work — see
	// package doc comment.
	windowMode := flag.Bool("window", false, "run as the Fyne window child process (otherwise: run the systray)")
	daemonAddr := flag.String("daemon-addr", ipc.DefaultAddr(), "goat-clientd IPC address")
	flag.Usage = func() { printUsage(os.Stderr) }
	flag.Parse()

	positional := flag.Args()
	if len(positional) > 0 {
		os.Exit(dispatchSubcommand(positional[0], positional[1:], *daemonAddr))
	}

	// No subcommand: GUI mode.
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	if *windowMode {
		log.SetPrefix("goat-client[window] ")
		if err := ui.RunWindow(*daemonAddr); err != nil {
			fmt.Fprintf(os.Stderr, "window: %v\n", err)
			os.Exit(1)
		}
		return
	}

	log.SetPrefix("goat-client[tray] ")
	if err := ui.RunTray(*daemonAddr); err != nil {
		fmt.Fprintf(os.Stderr, "tray: %v\n", err)
		os.Exit(1)
	}
}

// dispatchSubcommand routes a recognised subcommand name to its
// handler, threading the global daemon-addr default through.
// Unknown names print usage to stderr and return exit 2.
//
// Returns the desired process exit code; main wraps with os.Exit.
// Exposed for tests; not part of the binary's public surface.
func dispatchSubcommand(name string, args []string, defaultDaemonAddr string) int {
	switch name {
	case "getmode":
		return runGetMode(args, defaultDaemonAddr)
	case "setmode":
		return runSetMode(args, defaultDaemonAddr)
	case "connect":
		return runConnect(args, defaultDaemonAddr)
	case "disconnect":
		return runDisconnect(args, defaultDaemonAddr)
	case "status":
		return runStatus(args, defaultDaemonAddr)
	case "send":
		return runSend(args, defaultDaemonAddr)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", name)
		printUsage(os.Stderr)
		return 2
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "goat-client — desktop GUI + CLI for the goat overlay")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  goat-client                       launch the systray (GUI mode)")
	fmt.Fprintln(w, "  goat-client --window              launch the main window (child of systray)")
	fmt.Fprintln(w, "  goat-client getmode               print the daemon's active v0.2 mode")
	fmt.Fprintln(w, "  goat-client setmode <mode>        switch the daemon to <mode>")
	fmt.Fprintln(w, "  goat-client connect               bring the active mode's subsystems up")
	fmt.Fprintln(w, "  goat-client disconnect            tear the active mode's subsystems down")
	fmt.Fprintln(w, "  goat-client status                print the current status snapshot")
	fmt.Fprintln(w, "  goat-client send <peer> <file>    drop a file to an inner-mesh peer (goatdrop)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Modes: wg-cp0-only | netbird-only | combined")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Common flags (accepted before OR after the subcommand):")
	fmt.Fprintln(w, "  --daemon-addr=ADDR    override the daemon IPC endpoint")
}

func runGetMode(args []string, defaultDaemonAddr string) int {
	fs := flag.NewFlagSet("getmode", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", defaultDaemonAddr, "goat-clientd IPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client, err := ipc.NewClient(*daemonAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "getmode: dial daemon: %v\n", err)
		return 1
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := client.GetMode(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "getmode: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, m)
	return 0
}

func runSetMode(args []string, defaultDaemonAddr string) int {
	fs := flag.NewFlagSet("setmode", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", defaultDaemonAddr, "goat-clientd IPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: goat-client setmode <wg-cp0-only|netbird-only|combined>")
		return 2
	}
	m, err := mode.Parse(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "setmode: %v\n", err)
		return 2
	}
	client, err := ipc.NewClient(*daemonAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "setmode: dial daemon: %v\n", err)
		return 1
	}
	defer client.Close()
	// Mode switches budget ~30s for the reconcile per the verdict gate.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prev, err := client.SetMode(ctx, m.String())
	if err != nil {
		fmt.Fprintf(os.Stderr, "setmode: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "mode: %s → %s\n", prev, m)
	return 0
}

func runConnect(args []string, defaultDaemonAddr string) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", defaultDaemonAddr, "goat-clientd IPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client, err := ipc.NewClient(*daemonAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: dial daemon: %v\n", err)
		return 1
	}
	defer client.Close()
	// Connect blocks for both legs to come up in combined mode; the
	// daemon's own deadline is 30s per leg, so budget 60s here.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "connected")
	return 0
}

func runDisconnect(args []string, defaultDaemonAddr string) int {
	fs := flag.NewFlagSet("disconnect", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", defaultDaemonAddr, "goat-clientd IPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client, err := ipc.NewClient(*daemonAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "disconnect: dial daemon: %v\n", err)
		return 1
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Disconnect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "disconnect: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "disconnected")
	return 0
}

func runStatus(args []string, defaultDaemonAddr string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", defaultDaemonAddr, "goat-clientd IPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client, err := ipc.NewClient(*daemonAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: dial daemon: %v\n", err)
		return 1
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := client.GetStatus(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "mode:           %s\n", st.Mode)
	fmt.Fprintf(os.Stdout, "state:          %s\n", st.State)
	fmt.Fprintf(os.Stdout, "bundle:         %t\n", st.BundleImported)
	if st.Bundle != nil {
		fmt.Fprintf(os.Stdout, "issued-to:      %s\n", st.Bundle.IssuedTo)
		fmt.Fprintf(os.Stdout, "site:           %s\n", st.Bundle.Site)
		fmt.Fprintf(os.Stdout, "expires:        %s\n", st.Bundle.NotAfter.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(os.Stdout, "bytes-in:       %d\n", st.BytesIn)
	fmt.Fprintf(os.Stdout, "bytes-out:      %d\n", st.BytesOut)
	if !st.LastHandshake.IsZero() {
		fmt.Fprintf(os.Stdout, "last-handshake: %s\n", st.LastHandshake.UTC().Format(time.RFC3339))
	}
	if st.InnerMesh != nil {
		fmt.Fprintf(os.Stdout, "inner-mesh:     state=%s peers=%d in=%d out=%d\n",
			st.InnerMesh.State, st.InnerMesh.PeerCount, st.InnerMesh.BytesIn, st.InnerMesh.BytesOut)
	}
	if st.ErrorMessage != "" {
		fmt.Fprintf(os.Stdout, "error:          %s\n", st.ErrorMessage)
	}
	return 0
}

// runSend drops a local file to an inner-mesh peer via goatdrop:
//
//	goat-client send <peer> <file>
//
// <peer> is the peer's overlay IP or its mesh name; <file> is a local path
// the daemon can read.
func runSend(args []string, defaultDaemonAddr string) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", defaultDaemonAddr, "goat-clientd IPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: goat-client send <peer> <file>")
		return 2
	}
	peer, path := rest[0], rest[1]

	client, err := ipc.NewClient(*daemonAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: dial daemon: %v\n", err)
		return 1
	}
	defer client.Close()
	// The tunnel, not the app, bounds a large transfer; give it room.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	res, err := client.SendFile(ctx, peer, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "sent %s (%d bytes) to %s\n", res.Name, res.Size, res.ToPeer)
	return 0
}
