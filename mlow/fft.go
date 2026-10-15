package mlow

import (
	"math"
	"sync"
)

// cpx is a single-precision complex value.
//
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L318-L343
type cpx struct {
	re, im float32
}

func (a cpx) add(b cpx) cpx {
	return cpx{re: a.re + b.re, im: a.im + b.im}
}

func (a cpx) mul(b cpx) cpx {
	return cpx{
		re: a.re*b.re - a.im*b.im,
		im: a.re*b.im + a.im*b.re,
	}
}

// smallestFactor returns the smallest prime factor of n (>= 2).
func smallestFactor(n int) int {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L346-L358
	if n%2 == 0 {
		return 2
	}
	p := 3
	for p*p <= n {
		if n%p == 0 {
			return p
		}
		p += 2
	}
	return n
}

// fftLevel is one split of the factorization chain: at length n the recursion
// splits into p sub-DFTs of length m = n/p. Every node at a given depth sees the
// same n, so the chain is a flat list indexed by depth. tw holds the combine
// twiddles indexed k*p + q.
type fftLevel struct {
	n, p, m int
	tw      []cpx
}

// fftPlan is the input-independent part of one (length, sign) transform: the
// factorization chain, every twiddle it reads, and the scratch it runs in. Each
// twiddle is computed with the exact arithmetic the butterflies used inline, so
// reading the table is bit-identical to recomputing it.
type fftPlan struct {
	levels []fftLevel
	baseN  int
	base   []cpx // prime-base DFT twiddles, indexed k*baseN + j
	arena  int   // scratch length fftRec needs below the top level
	pool   sync.Pool
}

type fftPlanKey struct {
	n    int
	sign float32
}

var fftPlans sync.Map // fftPlanKey -> *fftPlan

// combineTwiddle is the combine-step twiddle for (n, k, q, sign).
func combineTwiddle(n, k, q int, sign float32) cpx {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L390-L396
	ang := sign * 2.0 * smplPI * float32(k) * float32(q) / float32(n)
	return cpx{re: float32(math.Cos(float64(ang))), im: float32(math.Sin(float64(ang)))}
}

// baseTwiddle is the prime-base DFT twiddle for (n, k, j, sign).
func baseTwiddle(n, k, j int, sign float32) cpx {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L401-L408
	angK := sign * 2.0 * smplPI * float32(k) / float32(n)
	ang := angK * float32(j)
	return cpx{re: float32(math.Cos(float64(ang))), im: float32(math.Sin(float64(ang)))}
}

// getFFTPlan returns the shared plan for a length-n transform of the given sign,
// building it on first use.
func getFFTPlan(n int, sign float32) *fftPlan {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L413-L443
	key := fftPlanKey{n, sign}
	if p, ok := fftPlans.Load(key); ok {
		return p.(*fftPlan)
	}
	plan := &fftPlan{baseN: 1}
	for size := n; size > 1; {
		p := smallestFactor(size)
		if p == size {
			plan.baseN = size
			plan.base = make([]cpx, 0, size*size)
			for k := 0; k < size; k++ {
				for j := 0; j < size; j++ {
					plan.base = append(plan.base, baseTwiddle(size, k, j, sign))
				}
			}
			break
		}
		tw := make([]cpx, 0, size*p)
		for k := 0; k < size; k++ {
			for q := 0; q < p; q++ {
				tw = append(tw, combineTwiddle(size, k, q, sign))
			}
		}
		plan.levels = append(plan.levels, fftLevel{n: size, p: p, m: size / p, tw: tw})
		plan.arena += size
		size /= p
	}
	actual, _ := fftPlans.LoadOrStore(key, plan)
	return actual.(*fftPlan)
}

// scratch borrows a buffer of at least size entries from the plan's pool.
func (p *fftPlan) scratch(size int) *[]cpx {
	if b, ok := p.pool.Get().(*[]cpx); ok && len(*b) >= size {
		return b
	}
	b := make([]cpx, size)
	return &b
}

// fftRec is the recursive mixed-radix Cooley-Tukey DFT. x holds the inputs at the
// given stride; out is contiguous. arena is the remaining scratch: each split level
// takes its first n entries for its sub-DFT outputs and hands the rest down
// (siblings run one after another, so they share it).
func (p *fftPlan) fftRec(x []cpx, stride, depth int, out, arena []cpx) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L554-L601
	if depth == len(p.levels) {
		n := p.baseN
		if n <= 1 {
			out[0] = x[0]
			return
		}
		for k := 0; k < n; k++ {
			var acc cpx
			row := p.base[k*n : k*n+n]
			for j, w := range row {
				acc = acc.add(x[j*stride].mul(w))
			}
			out[k] = acc
		}
		return
	}
	lvl := &p.levels[depth]
	n, radix, m := lvl.n, lvl.p, lvl.m
	sub, rest := arena[:n], arena[n:]
	for q := 0; q < radix; q++ {
		p.fftRec(x[q*stride:], stride*radix, depth+1, sub[q*m:(q+1)*m], rest)
	}
	for k := 0; k < n; k++ {
		kmod := k % m
		var acc cpx
		row := lvl.tw[k*radix : k*radix+radix]
		for q, w := range row {
			acc = acc.add(sub[q*m+kmod].mul(w))
		}
		out[k] = acc
	}
}

// cfft computes the complex FFT of a mixed-radix length into out. sign=-1 forward,
// +1 inverse.
func cfft(input, out []cpx, sign float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L604-L609
	plan := getFFTPlan(len(input), sign)
	arena := plan.scratch(plan.arena)
	plan.fftRec(input, 1, 0, out, *arena)
	plan.pool.Put(arena)
}

// rfftPlan is the input-independent part of a length-n real transform done as
// one length-n/2 complex transform plus an O(n) recombination: the recombination
// twiddles W_n^k for k < n/2 and the half-length complex plans.
type rfftPlan struct {
	wn       []cpx
	fwd, bwd *fftPlan
	pool     sync.Pool // *rfftScratch
}

type rfftScratch struct {
	z, zt, arena []cpx
}

var rfftPlans sync.Map // int -> *rfftPlan

func getRFFTPlan(n int) *rfftPlan {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L486-L499
	if p, ok := rfftPlans.Load(n); ok {
		return p.(*rfftPlan)
	}
	m := n / 2
	plan := &rfftPlan{wn: make([]cpx, m), fwd: getFFTPlan(m, -1.0), bwd: getFFTPlan(m, 1.0)}
	for k := range plan.wn {
		plan.wn[k] = combineTwiddle(n, k, 1, -1.0)
	}
	actual, _ := rfftPlans.LoadOrStore(n, plan)
	return actual.(*rfftPlan)
}

func (p *rfftPlan) scratch() *rfftScratch {
	if sc, ok := p.pool.Get().(*rfftScratch); ok {
		return sc
	}
	m := len(p.wn)
	return &rfftScratch{z: make([]cpx, m), zt: make([]cpx, m), arena: make([]cpx, p.fwd.arena)}
}

// rfftForwardOrdered is the forward real FFT of n real samples, re-packed into the
// ordered REAL layout: f[0]=DC.re, f[1]=Nyquist.re, then [re,im] pairs for bins
// 1..n/2-1. Output length is n. n must be even.
//
// Packing z[j] = x[2j] + i*x[2j+1] turns it into one length-n/2 complex transform;
// the even- and odd-sample spectra come back out of Z by its Hermitian split and
// are recombined with W_n^k.
func rfftForwardOrdered(time, f []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L628-L675
	n := len(time)
	if len(f) != n || n < 2 || n%2 != 0 {
		panic("mlow: rfft forward needs an even length >= 2 and an output of the same length")
	}
	plan := getRFFTPlan(n)
	sc := plan.scratch()
	m := n / 2
	for j := 0; j < m; j++ {
		sc.z[j] = cpx{re: time[2*j], im: time[2*j+1]}
	}
	plan.fwd.fftRec(sc.z, 1, 0, sc.zt, sc.arena)

	z0 := sc.zt[0]
	f[0] = z0.re + z0.im
	f[1] = z0.re - z0.im
	for k := 1; k < m; k++ {
		a := sc.zt[k]
		b := sc.zt[m-k]
		eRe := 0.5 * (a.re + b.re)
		eIm := 0.5 * (a.im - b.im)
		oRe := 0.5 * (a.im + b.im)
		oIm := 0.5 * (b.re - a.re)
		w := plan.wn[k]
		f[2*k] = eRe + (oRe*w.re - oIm*w.im)
		f[2*k+1] = eIm + (oRe*w.im + oIm*w.re)
	}
	plan.pool.Put(sc)
}

// rfftBackwardOrdered is the inverse real FFT from the ordered REAL layout,
// unnormalized (backward(forward(x)) = n*x), through one length-n/2 complex
// transform.
func rfftBackwardOrdered(f []float32, time []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/1f25f7e18faf328ed0dd8f57fc22db9fd44b1964/wacore/src/voip/mlow/smpl_perc.rs#L679-L730
	n := len(f)
	if len(time) != n || n < 2 || n%2 != 0 {
		panic("mlow: rfft backward needs an even length >= 2 and an output of the same length")
	}
	plan := getRFFTPlan(n)
	sc := plan.scratch()
	m := n / 2
	sc.z[0] = cpx{re: 0.5 * (f[0] + f[1]), im: 0.5 * (f[0] - f[1])}
	for k := 1; k < m; k++ {
		aRe := f[2*k]
		aIm := f[2*k+1]
		cRe := f[2*(m-k)]
		cIm := -f[2*(m-k)+1]
		eRe := 0.5 * (aRe + cRe)
		eIm := 0.5 * (aIm + cIm)
		dRe := 0.5 * (aRe - cRe)
		dIm := 0.5 * (aIm - cIm)
		w := plan.wn[k]
		oRe := dRe*w.re + dIm*w.im
		oIm := dIm*w.re - dRe*w.im
		sc.z[k] = cpx{re: eRe - oIm, im: eIm + oRe}
	}
	plan.bwd.fftRec(sc.z, 1, 0, sc.zt, sc.arena)
	for j := 0; j < m; j++ {
		time[2*j] = 2.0 * sc.zt[j].re
		time[2*j+1] = 2.0 * sc.zt[j].im
	}
	plan.pool.Put(sc)
}
