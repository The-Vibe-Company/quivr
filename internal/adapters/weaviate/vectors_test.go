package weaviate_test

import "math"

// unitVector returns a distinct normalized vector per seed.
func unitVector(seed int) []float32 {
	v := make([]float32, 384)
	n := 0.0
	for i := range v {
		v[i] = float32(math.Sin(float64(seed*7+i) + 0.5))
		n += float64(v[i]) * float64(v[i])
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / math.Sqrt(n))
	}
	return v
}
