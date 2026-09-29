package etcd

import (
	"slices"
	"testing"
)

func TestWithEndpointsSplitsAndTrims(t *testing.T) {
	registry, err := New(WithEndpoints("127.0.0.1:2379, 127.0.0.2:2379"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer registry.Close()

	want := []string{"127.0.0.1:2379", "127.0.0.2:2379"}
	if got := registry.client.Endpoints(); !slices.Equal(got, want) {
		t.Fatalf("Endpoints() = %v, want %v", got, want)
	}
}

func TestNewRejectsEmptyEndpoints(t *testing.T) {
	if _, err := New(WithEndpoints(" , ")); err == nil {
		t.Fatal("New() accepted empty endpoints")
	}
}
