// Command fake-relay runs the fake relay on loopback for trying the Link by hand. It prints a pairing code, as
// Firmary's Settings › Connections would (pairing v2: give it to `firmary-link pair --code`), confirms the pairing as
// soon as a Link claims it, then sends each line typed on stdin as a turn and prints the streamed reply. A line
// ":code" prints a new pairing code. Use synthetic prompts.
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

	"github.com/Firmary/bridge/internal/driver/claudecode"
	"github.com/Firmary/bridge/internal/fakerelay"
	"github.com/Firmary/bridge/internal/protocol"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "loopback address to listen on")
	keyOut := flag.String("ticket-key-file", filepath.Join(os.TempDir(), "fake-relay-ticket.pub"), "where to write the ticket public key")
	pin := flag.String("pin", claudecode.BuiltInPin.Version, "Claude Code version to put in tickets")
	person := flag.String("person", fakerelay.UserName, "the person the pairing code is for")
	firm := flag.String("firm", fakerelay.FirmName, "their firm")
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
	code := r.Issue(*person, *firm)
	fmt.Printf("pairing code: %s (for %s · %s, auto-confirmed once claimed)\n", code, *person, *firm)
	fmt.Printf("run: firmary-link pair --code %s --env local --relay-url %s --ticket-key-file %s\n", code, r.URL, *keyOut)

	go func() {
		for {
			select {
			case c := <-r.Codes:
				fmt.Println("pairing claimed and confirmed:", c)
			case st := <-r.SigninSteps:
				fmt.Printf("signin_step: %s %s %s %s\n", st.Provider, st.Kind, st.URL, st.UserCode)
			case h := <-r.Hellos:
				fmt.Printf("hello: %+v\n", h)
			}
		}
	}()

	in := bufio.NewScanner(os.Stdin)
	fmt.Println("type a prompt and press Enter")
	for in.Scan() {
		if in.Text() == ":code" {
			fmt.Println("pairing code:", r.Issue(*person, *firm))
			continue
		}
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
