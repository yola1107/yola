package service

import (
	"context"
	"testing"

	"yola/node"
	"yola/test/whot/api/v1"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/stretchr/testify/require"
)

func TestOnLoginReqValidatesAuthenticatedUID(t *testing.T) {
	for _, test := range []struct {
		name    string
		session node.Session
		request *v1.LoginReq
		reason  string
	}{
		{name: "missing session", request: &v1.LoginReq{UserID: 42}, reason: "INVALID_SESSION"},
		{name: "noncanonical session UID", session: loginSession("042"), request: &v1.LoginReq{UserID: 42}, reason: "INVALID_SESSION"},
		{name: "missing request", session: loginSession("42"), reason: "INVALID_REQUEST"},
		{name: "mismatched UID", session: loginSession("42"), request: &v1.LoginReq{UserID: 43}, reason: "UID_MISMATCH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.session != nil {
				ctx = node.NewContext(ctx, test.session)
			}
			_, err := new(Service).OnLoginReq(ctx, test.request)
			require.Equal(t, test.reason, errors.Reason(err))
		})
	}
}

func loginSession(uid string) node.Session {
	return identitySession{uid: uid}
}

type identitySession struct {
	node.Session
	uid string
}

func (sess identitySession) UID() string { return sess.uid }

func (identitySession) BindingToken() string { return "binding-test" }
