package mlow

import (
	"math"
	"testing"
)

func benchSpeech(n, off int) []float32 {
	out := make([]float32, n)
	for i := range out {
		t := float64(off+i) / 16000
		env := 0.5 + 0.5*math.Sin(2*math.Pi*3*t)
		out[i] = float32(env * (0.3*math.Sin(2*math.Pi*180*t) + 0.15*math.Sin(2*math.Pi*360*t) + 0.08*math.Sin(2*math.Pi*1100*t)))
	}
	return out
}

func BenchmarkEncode60ms(b *testing.B) {
	e := NewMlowEncoder()
	frames := make([][]float32, 50)
	for i := range frames {
		frames[i] = benchSpeech(960, i*960)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Encode(frames[i%50]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode60ms(b *testing.B) {
	e := NewMlowEncoder()
	var pkts [][]byte
	for i := 0; i < 50; i++ {
		p, _ := e.Encode(benchSpeech(960, i*960))
		pkts = append(pkts, append([]byte(nil), p...))
	}
	d := NewMlowDecoder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Decode(pkts[i%50])
	}
}
