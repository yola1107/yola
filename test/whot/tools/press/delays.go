package press

import (
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

type delayedJobs struct {
	mu      sync.Mutex
	jobs    map[*delayedJob]struct{}
	stopped bool
	wg      sync.WaitGroup
}

type delayedJob struct {
	timer *time.Timer
}

func (jobs *delayedJobs) After(delay time.Duration, callback func()) bool {
	if delay < 0 || callback == nil {
		return false
	}
	jobs.mu.Lock()
	if jobs.stopped {
		jobs.mu.Unlock()
		return false
	}
	if jobs.jobs == nil {
		jobs.jobs = make(map[*delayedJob]struct{})
	}
	job := new(delayedJob)
	jobs.wg.Add(1)
	job.timer = time.AfterFunc(delay, func() { jobs.run(job, callback) })
	jobs.jobs[job] = struct{}{}
	jobs.mu.Unlock()
	return true
}

func (jobs *delayedJobs) Stop() {
	jobs.mu.Lock()
	if !jobs.stopped {
		jobs.stopped = true
		for job := range jobs.jobs {
			delete(jobs.jobs, job)
			if job.timer.Stop() {
				jobs.wg.Done()
			}
		}
	}
	jobs.mu.Unlock()
	jobs.wg.Wait()
}

func (jobs *delayedJobs) run(job *delayedJob, callback func()) {
	defer jobs.wg.Done()
	defer func() {
		jobs.mu.Lock()
		delete(jobs.jobs, job)
		jobs.mu.Unlock()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("press delayed job panicked", "panic", recovered, "stack", string(debug.Stack()))
		}
	}()
	callback()
}
