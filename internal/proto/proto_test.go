package proto

import (
	"net"
	"testing"
)

func TestHandshakeRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := Handshake{
		Version:     Version,
		Token:       "lu_test123",
		Subdomain:   "demo",
		BufferRules: []string{"/webhooks/*", "/api/events"},
	}

	errc := make(chan error, 1)
	go func() { errc <- WriteJSON(client, want) }()

	var got Handshake
	if err := ReadJSON(server, &got); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	if got.Version != want.Version || got.Token != want.Token || got.Subdomain != want.Subdomain {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(got.BufferRules) != len(want.BufferRules) {
		t.Fatalf("buffer rules mismatch: got %v, want %v", got.BufferRules, want.BufferRules)
	}
	for i := range want.BufferRules {
		if got.BufferRules[i] != want.BufferRules[i] {
			t.Fatalf("buffer rule %d mismatch: got %q, want %q", i, got.BufferRules[i], want.BufferRules[i])
		}
	}
}

func TestHandshakeReplyRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := HandshakeReply{OK: true, Subdomain: "demo", PublicURL: "http://demo.lvh.me:8080"}

	errc := make(chan error, 1)
	go func() { errc <- WriteJSON(server, want) }()

	var got HandshakeReply
	if err := ReadJSON(client, &got); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHandshakeReplyRejection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := HandshakeReply{OK: false, Error: "invalid auth token"}
	go WriteJSON(server, want)

	var got HandshakeReply
	if err := ReadJSON(client, &got); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if got.OK {
		t.Fatalf("expected rejection, got OK reply: %+v", got)
	}
	if got.Error != want.Error {
		t.Fatalf("got error %q, want %q", got.Error, want.Error)
	}
}
