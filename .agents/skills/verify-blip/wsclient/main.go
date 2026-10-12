// Command wsclient drives a running Blip server the way two browser tabs do
// and prints one line per observation for verify.sh to assert on.
//
// Usage: go run ./.agents/skills/verify-blip/wsclient -addr 127.0.0.1:PORT -origin http://localhost:PORT
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func dial(url, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial(url, h)
}

func main() {
	addr := flag.String("addr", "", "host:port of the server")
	origin := flag.String("origin", "", "Origin header to send")
	payload := flag.String("payload", `{"content":"<hi> & bye","extra":1}`, "frame the sender sends")
	flag.Parse()
	url := "ws://" + *addr + "/ws"

	_, resp, err := dial(url, "")
	if resp != nil {
		fmt.Printf("no-origin handshake status: %d\n", resp.StatusCode)
	} else {
		fmt.Printf("no-origin handshake error: %v\n", err)
	}

	sender, _, err := dial(url, *origin)
	if err != nil {
		fmt.Printf("sender dial failed: %v\n", err)
		os.Exit(1)
	}
	defer sender.Close()
	receiver, _, err := dial(url, *origin)
	if err != nil {
		fmt.Printf("receiver dial failed: %v\n", err)
		os.Exit(1)
	}
	defer receiver.Close()
	fmt.Println("both clients connected")
	time.Sleep(200 * time.Millisecond)

	if err := sender.WriteMessage(websocket.TextMessage, []byte(*payload)); err != nil {
		fmt.Printf("send failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("sent: %s\n", *payload)

	_ = receiver.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, msg, err := receiver.ReadMessage(); err != nil {
		fmt.Printf("receiver got nothing: %v\n", err)
	} else {
		fmt.Printf("receiver got: %s\n", msg)
	}

	_ = sender.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, msg, err := sender.ReadMessage(); err != nil {
		fmt.Println("sender got nothing (expected)")
	} else {
		fmt.Printf("sender got its own message: %s\n", msg)
	}
}
