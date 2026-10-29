package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHookPayload(t *testing.T) {
	payload, key, err := hookPayload("", "")
	if err != nil || payload != "" || key != "" {
		t.Fatalf("empty key %q %q %v", payload, key, err)
	}
	payload, key, err = hookPayload("private-key", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 || key == "private-key" {
		t.Fatalf("guac key %q", key)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatal(err)
	}
	if body["serverPrivateKey"] != "private-key" || body["peerPublicKey"] != defaultPeerPublicKey || body["hostAddr"] != "127.0.0.1" || body["guacCryptKey"] != key {
		t.Fatalf("payload %#v", body)
	}
}

func TestWebsocketURL(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"abc.example":                "wss://abc.example/",
		"https://abc.example":        "wss://abc.example/",
		"https://abc.example/socket": "wss://abc.example/socket",
		"http://abc.example/socket":  "ws://abc.example/socket",
	}
	for in, want := range cases {
		if got := websocketURL(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestFormatStarted(t *testing.T) {
	if got := formatStarted([]byte("1700000000")); got != "2023-11-14T22:13:20Z" {
		t.Fatalf("seconds %q", got)
	}
	if got := formatStarted([]byte("1700000000000")); !strings.HasPrefix(got, "2023-11-14T22:13:20") {
		t.Fatalf("millis %q", got)
	}
	if formatStarted(nil) != "" || formatStarted([]byte("null")) != "" {
		t.Fatal("empty timestamp")
	}
}
