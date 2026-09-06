package press

import (
	"testing"
	"time"
)

func TestDelaysCancelPendingAndWaitForRunningJob(t *testing.T) {
	var scheduler delayedJobs
	canceled := make(chan struct{}, 1)
	scheduler.After(time.Hour, func() { canceled <- struct{}{} })
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler.After(0, func() {
		close(started)
		<-release
	})
	<-started
	stopped := make(chan struct{})
	go func() {
		scheduler.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned before the running job completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not join the running job")
	}
	select {
	case <-canceled:
		t.Fatal("pending job ran after Stop")
	default:
	}
	if scheduler.After(time.Second, func() {}) {
		t.Fatal("After accepted a job after Stop")
	}
}
