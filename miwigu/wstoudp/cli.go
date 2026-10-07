//go:build !windows

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	url := flag.String("url", "", "WebSocket URL")
	token := flag.String("token", "", "Lambda MicroVM auth token")
	listen := flag.String("listen", listenAddr, "UDP address WireGuard sends to")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: wstoudp -url wss://host/path -token TOKEN\n")
		fmt.Fprintf(os.Stderr, "\nListens for WireGuard on UDP and carries each datagram over the WebSocket.\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 0 || *url == "" || *token == "" {
		flag.Usage()
		os.Exit(2)
	}

	br, err := newBridge(*listen, func(text string) {
		log.Println(text)
	})
	if err != nil {
		log.Fatal(err)
	}
	defer br.Close()

	br.setTarget(*url, *token)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
