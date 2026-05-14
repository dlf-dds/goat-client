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
//   goat-client getmode             — print the active mode + exit
//   goat-client setmode <mode>      — switch mode + exit (re-uses daemon IPC)
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

func main() {
	// Subcommand dispatch happens before flag parsing because Go's flag
	// package consumes os.Args[1:] linearly. `goat-client getmode` and
	// `goat-client setmode <mode>` use a tiny hand-rolled dispatcher.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "getmode":
			os.Exit(runGetMode(os.Args[2:]))
		case "setmode":
			os.Exit(runSetMode(os.Args[2:]))
		case "help", "-h", "--help":
			printUsage(os.Stdout)
			return
		}
	}

	var (
		windowMode = flag.Bool("window", false, "run as the Fyne window child process (otherwise: run the systray)")
		daemonAddr = flag.String("daemon-addr", ipc.DefaultAddr(), "goat-clientd IPC address")
	)
	flag.Parse()

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

func printUsage(w *os.File) {
	fmt.Fprintln(w, "goat-client — desktop GUI + CLI for the goat overlay")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  goat-client                       launch the systray (GUI mode)")
	fmt.Fprintln(w, "  goat-client --window              launch the main window (child of systray)")
	fmt.Fprintln(w, "  goat-client getmode               print the daemon's active v0.2 mode")
	fmt.Fprintln(w, "  goat-client setmode <mode>        switch the daemon to <mode>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Modes: wg-cp0-only | netbird-only | combined")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Common flags:")
	fmt.Fprintln(w, "  --daemon-addr=ADDR    override the daemon IPC endpoint")
}

func runGetMode(args []string) int {
	fs := flag.NewFlagSet("getmode", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", ipc.DefaultAddr(), "goat-clientd IPC address")
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

func runSetMode(args []string) int {
	fs := flag.NewFlagSet("setmode", flag.ContinueOnError)
	daemonAddr := fs.String("daemon-addr", ipc.DefaultAddr(), "goat-clientd IPC address")
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
