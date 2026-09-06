package main

import (
	"context"
	"errors"
	"testing"

	"yola/examples/message"
	"yola/gateway"
)

func TestAuthenticatorPreservesPlayerUID(t *testing.T) {
	auth := authenticator{}
	for _, want := range []string{"player-1", "player-2"} {
		token := message.AuthTokenPrefix + want
		got, err := auth.Authenticate(context.Background(), "whot", []byte(token), "127.0.0.1")
		if err != nil || got != want {
			t.Fatalf("Authenticate() = %q, %v, want %q, nil", got, err, want)
		}
	}
	if _, err := auth.Authenticate(context.Background(), "whot", []byte("wrong:player-1"), "127.0.0.1"); !errors.Is(err, gateway.ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want %v", err, gateway.ErrInvalidCredentials)
	}
}
