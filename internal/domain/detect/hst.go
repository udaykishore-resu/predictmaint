package detect

import (
	"hash/fnv"
	"math"
	"math/rand"
)

// HSTParams configure the streaming Half-Space Trees detector
// (Tan, Ting & Liu, 2011): a fixed random forest of axis-aligned half-space
// splits in the standardised feature cube, with reference/latest mass
// counters per node that roll over every WindowSize points.
type HSTParams struct {
	Trees      int   `json:"trees"`
	Depth      int   `json:"depth"`
	WindowSize int   `json:"window_size"`
	Seed       int64 `json:"seed"`
	// ZRange is how many robust sigmas map to the edge of the unit cube.
	ZRange float64 `json:"z_range"`
}

func (p HSTParams) withDefaults() HSTParams {
	if p.Trees <= 0 {
		p.Trees = 25
	}
	if p.Depth <= 0 {
		p.Depth = 6
	}
	if p.Depth > 12 {
		p.Depth = 12
	}
	if p.WindowSize <= 0 {
		p.WindowSize = 64
	}
	if p.ZRange <= 0 {
		p.ZRange = 4
	}
	return p
}

type hstNode struct {
	dim         int
	split       float64
	left, right *hstNode
	r, l        int // reference mass, latest mass
}

// HST is a per-asset streaming Half-Space Trees detector.
type HST struct {
	p        HSTParams
	features []string
	base     Baselines
	zp       RobustZParams
	trees    []*hstNode
	seen     int
	ready    bool
	point    []float64
}

// HSTResult is the detector output.
type HSTResult struct {
	Score   float64 `json:"score"`
	Density float64 `json:"density"` // 1 = uniform background, >1 = dense/normal
	Ready   bool    `json:"ready"`
	Rule    string  `json:"rule"`
}

// NewHST builds the forest. features is the ordered list of feature names
// used as dimensions; seedKey (e.g. the asset id) makes the forest
// reproducible per asset. Baselines standardise raw features into the cube.
func NewHST(p HSTParams, features []string, seedKey string, base Baselines, zp RobustZParams) *HST {
	p = p.withDefaults()
	d := len(features)
	if d == 0 {
		d = 1
	}
	h := &HST{p: p, features: features, base: base, zp: zp, point: make([]float64, d)}
	for i := range h.point {
		h.point[i] = 0.5
	}
	seed := p.Seed
	if seed == 0 {
		f := fnv.New64a()
		_, _ = f.Write([]byte(seedKey))
		seed = int64(f.Sum64() & math.MaxInt64)
	}
	rng := rand.New(rand.NewSource(seed))
	h.trees = make([]*hstNode, p.Trees)
	for i := range h.trees {
		lo := make([]float64, d)
		hi := make([]float64, d)
		for q := 0; q < d; q++ {
			sq := rng.Float64()
			span := 2 * math.Max(sq, 1-sq)
			lo[q] = sq - span
			hi[q] = sq + span
		}
		h.trees[i] = buildHST(rng, lo, hi, 0, p.Depth)
	}
	return h
}

func buildHST(rng *rand.Rand, lo, hi []float64, depth, maxDepth int) *hstNode {
	if depth == maxDepth {
		return &hstNode{dim: -1}
	}
	q := rng.Intn(len(lo))
	split := (lo[q] + hi[q]) / 2
	n := &hstNode{dim: q, split: split}
	oldHi := hi[q]
	hi[q] = split
	n.left = buildHST(rng, lo, hi, depth+1, maxDepth)
	hi[q] = oldHi
	oldLo := lo[q]
	lo[q] = split
	n.right = buildHST(rng, lo, hi, depth+1, maxDepth)
	lo[q] = oldLo
	return n
}

// SetBaselines replaces the standardisation baselines. Because the cube
// coordinates change, accumulated masses are discarded and the detector
// re-enters its warm-up window.
func (h *HST) SetBaselines(base Baselines) {
	h.base = base
	h.seen = 0
	h.ready = false
	for _, t := range h.trees {
		resetMass(t)
	}
}

func resetMass(n *hstNode) {
	if n == nil {
		return
	}
	n.r, n.l = 0, 0
	resetMass(n.left)
	resetMass(n.right)
}

// Ready reports whether a full reference window has been accumulated.
func (h *HST) Ready() bool { return h.ready }

func (h *HST) standardise(features map[string]float64) {
	for i, name := range h.features {
		v, ok := features[name]
		b, okb := h.base[name]
		if !ok || !okb {
			h.point[i] = 0.5
			continue
		}
		z := h.zp.Z(v, b)
		h.point[i] = clamp01(0.5 + z/(2*h.p.ZRange))
	}
}

// Observe scores the point against the reference window, then adds it to the
// latest window. Scoring before the first reference window is complete
// yields Ready=false and Score=0.
func (h *HST) Observe(features map[string]float64) HSTResult {
	h.standardise(features)
	res := HSTResult{Rule: RuleHST, Ready: h.ready}
	leafScale := math.Pow(2, float64(h.p.Depth))
	var dens float64
	for _, root := range h.trees {
		n := root
		for n.dim >= 0 {
			n.l++
			if h.point[n.dim] < n.split {
				n = n.left
			} else {
				n = n.right
			}
		}
		n.l++
		dens += float64(n.r) * leafScale / float64(h.p.WindowSize)
	}
	dens /= float64(len(h.trees))
	res.Density = dens
	if h.ready {
		res.Score = clamp01(1 / (1 + dens))
	}
	h.seen++
	if h.seen >= h.p.WindowSize {
		for _, t := range h.trees {
			rollover(t)
		}
		h.seen = 0
		h.ready = true
	}
	return res
}

func rollover(n *hstNode) {
	if n == nil {
		return
	}
	n.r = n.l
	n.l = 0
	rollover(n.left)
	rollover(n.right)
}
