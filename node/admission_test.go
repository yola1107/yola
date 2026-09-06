package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestAdmissionStopClosesEmptyAdmission(t *testing.T) {
	var admission requestAdmission
	require.NoError(t, admission.stopAndWait(context.Background()))
	require.False(t, admission.admit())
	require.NoError(t, admission.stopAndWait(context.Background()))
}

func TestRequestAdmissionTimeoutKeepsAcceptedWorkTracked(t *testing.T) {
	var admission requestAdmission
	require.True(t, admission.admit())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, admission.stopAndWait(ctx), context.Canceled)
	require.False(t, admission.admit())
	admission.done()
	require.NoError(t, admission.stopAndWait(ctx))
	require.False(t, admission.admit())
}
