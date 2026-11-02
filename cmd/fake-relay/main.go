// Command fake-relay runs the fake relay on loopback for trying the Helper by hand. It pairs the first Helper that
// connects, then sends each line typed on stdin as a turn and prints the streamed reply. Use synthetic prompts.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Firmary/firmary-helper/internal/driver/claudecode"
	"github.com/Firmary/firmary-helper/internal/fakerelay"
	"github.com/Firmary/firmary-helper/internal/protocol"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "loopback address to listen on")
	keyOut := flag.String("ticket-key-file", filepath.Join(os.TempDir(), "fake-relay-ticket.pub"), "where to write the ticket public key")
	pin := flag.String("pin", claudecode.BuiltInPin.Version, "Claude Code version to put in tickets")
	flag.Parse()

	r, err := fakerelay.Start(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer r.Close()
	r.AutoConfirm = true
	if err := os.WriteFile(*keyOut, []byte(base64.StdEncoding.EncodeToString(r.TicketPub)+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("fake relay: %s\nticket key: %s\n", r.URL, *keyOut)
	fmt.Printf("run: firmary-helper run --env local --relay-url %s --ticket-key-file %s\n", r.URL, *keyOut)

	go func() {
		for {
			select {
			case c := <-r.Codes:
				fmt.Println("pairing code (auto-confirmed):", c)
			case h := <-r.Hellos:
				fmt.Printf("hello: %+v\n", h)
			}
		}
	}()

	in := bufio.NewScanner(os.Stdin)
	fmt.Println("type a prompt and press Enter")
	for in.Scan() {
		dev, err := r.WaitConnected(context.Background())
		if err != nil {
			continue
		}
		c := r.Claims(dev, *pin, "", in.Text())
		if err := r.SendTurn(c, "", in.Text()); err != nil {
			fmt.Fprintln(os.Stderr, "send:", err)
			continue
		}
		timeout := time.After(5 * time.Minute)
	wait:
		for {
			select {
			case m := <-r.Events:
				switch m.Event.T {
				case protocol.EvTextDelta:
					fmt.Print(m.Event.Text)
				case protocol.EvDone, protocol.EvError:
					fmt.Printf("\n[%s %s %s]\n", m.Event.T, m.Event.Class, m.Event.Pattern)
					break wait
				default:
					fmt.Printf("[%s]", m.Event.T)
				}
			case <-timeout:
				fmt.Println("\n[timed out]")
				break wait
			}
		}
	}
}
