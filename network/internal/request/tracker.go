package request

import (
	"context"
	"sync"
	"sync/atomic"

	"yola/api/protocol/v1"
)

type Tracker struct {
	seq     atomic.Uint32
	pending sync.Map
}

type Pending struct {
	tracker *Tracker
	seq     int32
	done    chan outcome
}

type outcome struct {
	body []byte
	code int32
	err  error
}

func (t *Tracker) Begin(command int32, body []byte) (*v1.Proto, *Pending) {
	for {
		pending := &Pending{
			tracker: t,
			seq:     t.nextSequence(),
			done:    make(chan outcome, 1),
		}
		if _, loaded := t.pending.LoadOrStore(pending.seq, pending); loaded {
			continue
		}
		return &v1.Proto{Op: v1.OpRequest, Seq: pending.seq, Cmd: command, Body: body}, pending
	}
}

func (p *Pending) Cancel() {
	p.tracker.pending.CompareAndDelete(p.seq, p)
}

func (p *Pending) Wait(ctx context.Context) ([]byte, int32, error) {
	select {
	case result := <-p.done:
		return result.body, result.code, result.err
	case <-ctx.Done():
		if p.tracker.pending.CompareAndDelete(p.seq, p) {
			return nil, 0, ctx.Err()
		}
		result := <-p.done
		return result.body, result.code, result.err
	}
}

func (t *Tracker) Resolve(response *v1.Proto) bool {
	pending, ok := t.pending.LoadAndDelete(response.Seq)
	if !ok {
		return false
	}
	pending.(*Pending).done <- outcome{body: response.Body, code: response.Code}
	return true
}

func (t *Tracker) Fail(err error) {
	t.pending.Range(func(seq, pending any) bool {
		if t.pending.CompareAndDelete(seq, pending) {
			pending.(*Pending).done <- outcome{err: err}
		}
		return true
	})
}

func (t *Tracker) nextSequence() int32 {
	for {
		if seq := t.seq.Add(1) & uint32(1<<31-1); seq != 0 {
			return int32(seq)
		}
	}
}
