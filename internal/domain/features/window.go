// Package features turns raw, unit-tagged sensor samples into the rolling
// statistical features the detectors consume. It is pure computation: no I/O,
// no clocks, no allocation beyond the ring buffers it owns.
package features

// Window is a fixed-capacity ring buffer of float64 samples. Once full, each
// Push evicts the oldest sample. Values are returned oldest-first.
type Window struct {
	buf  []float64
	head int // index of the oldest sample when full, or next write slot
	n    int
}

// NewWindow returns a window that holds at most capacity samples.
// capacity < 1 is coerced to 1 so the zero-ish window is still usable.
func NewWindow(capacity int) *Window {
	if capacity < 1 {
		capacity = 1
	}
	return &Window{buf: make([]float64, capacity)}
}

// Push appends a sample, evicting the oldest when the window is full.
func (w *Window) Push(v float64) {
	w.buf[w.head] = v
	w.head = (w.head + 1) % len(w.buf)
	if w.n < len(w.buf) {
		w.n++
	}
}

// Len is the number of samples currently held.
func (w *Window) Len() int { return w.n }

// Cap is the window capacity.
func (w *Window) Cap() int { return len(w.buf) }

// Full reports whether the window has reached capacity.
func (w *Window) Full() bool { return w.n == len(w.buf) }

// Values copies the samples oldest-first into dst (allocating when dst is too
// small) and returns the slice.
func (w *Window) Values(dst []float64) []float64 {
	if cap(dst) < w.n {
		dst = make([]float64, w.n)
	}
	dst = dst[:w.n]
	if w.n < len(w.buf) {
		copy(dst, w.buf[:w.n])
		return dst
	}
	k := copy(dst, w.buf[w.head:])
	copy(dst[k:], w.buf[:w.head])
	return dst
}

// Reset drops all samples.
func (w *Window) Reset() {
	w.head = 0
	w.n = 0
}
