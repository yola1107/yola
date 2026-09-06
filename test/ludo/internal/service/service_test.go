package service

import (
	"context"
	"testing"

	"yola/node"
)

type identitySession struct {
	node.Session
	uid string
}

func (sess identitySession) UID() string { return sess.uid }

func (identitySession) BindingToken() string { return "binding-test" }

func TestAuthenticatedUIDRequiresCanonicalPositiveInteger(t *testing.T) {
	if _, err := authenticatedUID(context.Background()); err == nil {
		t.Fatal("authenticatedUID without Session returned nil error")
	}
	for _, rawUID := range []string{"", "0", "-1", "042", "+42", "player-42"} {
		t.Run(rawUID, func(t *testing.T) {
			ctx := node.NewContext(context.Background(), identitySession{uid: rawUID})
			if uid, err := authenticatedUID(ctx); err == nil {
				t.Fatalf("authenticatedUID(%q) = %d, want error", rawUID, uid)
			}
		})
	}
	ctx := node.NewContext(context.Background(), identitySession{uid: "42"})
	if uid, err := authenticatedUID(ctx); err != nil || uid != 42 {
		t.Fatalf("authenticatedUID(42) = (%d, %v)", uid, err)
	}
}

func TestRequestUIDRejectsClaimedIdentityMismatch(t *testing.T) {
	ctx := node.NewContext(context.Background(), identitySession{uid: "42"})
	if uid, err := requestUID(ctx, 43); err == nil {
		t.Fatalf("requestUID() = %d, want mismatch error", uid)
	}
}
