// Command firmary-helper is the Firmary Helper.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Firmary/firmary-helper/internal/driver/claudecode"
	"github.com/Firmary/firmary-helper/internal/helper"
	"github.com/Firmary/firmary-helper/internal/logx"
	"github.com/Firmary/firmary-helper/internal/relay"
	"github.com/Firmary/firmary-helper/internal/ticket"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// environment is one Firmary environment: its relay and the ticket-signing public keys pinned for it (current and
// next). The relays and keys don't exist yet (slice S3); with no pinned key the Helper refuses every turn.
type environment struct {
	relayURL   string
	ticketKeys []string // base64 Ed25519 public keys
}

var environments = map[string]environment{
	"prod": {relayURL: "wss://relay.prod.firmary.app/"},
	"dev":  {relayURL: "wss://relay.dev.firmary.app/"},
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "run":
		if err := run(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "firmary-helper:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: firmary-helper run [--env prod|dev|local] [--data-dir DIR] [--ring stable|internal]\n"+
		"                           [--relay-url ws://127.0.0.1:PORT/ --ticket-key-file FILE]   (local only)\n"+
		"       firmary-helper version")
	os.Exit(2)
}

func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Firmary Helper")
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Firmary", "Helper")
		}
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "firmary-helper")
	}
	return filepath.Join(home, ".local", "share", "firmary-helper")
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	envName := fs.String("env", "prod", "Firmary environment: prod, dev, or local (a fake relay on this computer)")
	dataDir := fs.String("data-dir", defaultDataDir(), "where the Helper keeps its key, pairing, logs and provider copies")
	ring := fs.String("ring", "stable", "update ring reported in hello")
	relayURL := fs.String("relay-url", "", "local only: the fake relay's loopback URL")
	keyFile := fs.String("ticket-key-file", "", "local only: file with the fake relay's base64 ticket public key")
	fs.Parse(args)

	var env environment
	if *envName == "local" {
		if !isLoopbackURL(*relayURL) || *keyFile == "" {
			return errors.New("--env local needs --relay-url on a loopback address and --ticket-key-file")
		}
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		env = environment{relayURL: *relayURL, ticketKeys: []string{strings.TrimSpace(string(b))}}
	} else {
		var ok bool
		if env, ok = environments[*envName]; !ok {
			return fmt.Errorf("unknown environment %q", *envName)
		}
		if *relayURL != "" || *keyFile != "" {
			return errors.New("--relay-url and --ticket-key-file are only for --env local")
		}
	}
	var keys []ed25519.PublicKey
	for _, k := range env.ticketKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return errors.New("ticket key is malformed")
		}
		keys = append(keys, b)
	}

	root := *dataDir
	envDir := filepath.Join(root, *envName)
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		return err
	}
	log, err := logx.Open(filepath.Join(root, "logs"))
	if err != nil {
		return err
	}
	defer log.Close()
	log.Log(logx.HelperStart, logx.Fields{Version: version})
	defer log.Log(logx.HelperStop, logx.Fields{Version: version})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	drv, err := claudecode.New(filepath.Join(root, "providers", claudecode.ID))
	if err != nil {
		return err
	}
	pin := claudecode.BuiltInPin
	fmt.Printf("Checking Claude Code %s ...\n", pin.Version)
	if err := drv.Ensure(ctx, pin); err != nil {
		log.Log(logx.ProviderEnsure, logx.Fields{Provider: claudecode.ID, Version: pin.Version, Reason: "failed"})
		fmt.Fprintln(os.Stderr, "Claude Code could not be installed:", err)
	} else {
		log.Log(logx.ProviderEnsure, logx.Fields{Provider: claudecode.ID, Version: pin.Version, Reason: "ok"})
	}

	h := &helper.Helper{Driver: drv, Pin: pin, Verifier: ticket.NewVerifier(keys, nil), DataDir: envDir,
		Version: version, Ring: *ring, Log: log}
	client, err := relay.New(relay.Config{
		URL: env.relayURL, Env: *envName, HelperVersion: version, Log: log, Handler: h,
		KeyPath: filepath.Join(envDir, "device.key"), PairingPath: filepath.Join(envDir, "pairing.json"),
		ShowPairingCode: func(code string) {
			fmt.Printf("\nPairing code: %s\nConfirm that Firmary shows the same code, then approve it there.\n\n", code)
		},
		ShowPaired: func(firmName string) {
			fmt.Printf("Paired to %s. If that is not your firm, stop the Helper and unpair it in Firmary.\n", firmName)
		},
	})
	if err != nil {
		return err
	}
	fmt.Println("Connecting to", env.relayURL)
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
