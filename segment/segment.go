package segment

import (
	"sync"
	"time"
)

type Segment struct {
	TS     time.Time
	Data   []byte
	Frames int
}

type Ring struct {
	segments []Segment
	oldest   int
	max      int
	mu       sync.Mutex
}

func NewRing(max int) *Ring {
	return &Ring{
		segments: make([]Segment, 0, max),
		oldest:   0,
		max:      max,
	}
}

func (r *Ring) Push(s Segment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.segments) < r.max {
		r.segments = append(r.segments, s)
		return
	}

	r.segments[r.oldest] = s
	r.oldest = (r.oldest + 1) % r.max
}

func (r *Ring) Segments() []Segment {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Segment, 0, len(r.segments))
	for i := 0; i < len(r.segments); i++ {
		idx := (i + r.oldest) % r.max
		out = append(out, r.segments[idx])
	}

	return out
}
