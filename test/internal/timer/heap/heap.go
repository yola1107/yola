package timerheap

import (
	"container/heap"
	"sync/atomic"
	"time"

	"yola/test/internal/timer"
)

type taskEntry struct {
	id       timer.TaskID
	execAt   time.Time
	interval time.Duration
	repeated bool
	canceled atomic.Bool
	callback func()
	index    int
}

type taskHeap []*taskEntry

func (h taskHeap) Len() int           { return len(h) }
func (h taskHeap) Less(i, j int) bool { return h[i].execAt.Before(h[j].execAt) }
func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(value any) {
	entry := value.(*taskEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *taskHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.index = -1
	*h = old[:last]
	return entry
}

func (h *taskHeap) remove(entry *taskEntry) {
	if entry.index >= 0 && entry.index < h.Len() && (*h)[entry.index] == entry {
		heap.Remove(h, entry.index)
	}
}
