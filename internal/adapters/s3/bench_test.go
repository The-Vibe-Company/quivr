package s3_test

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Run with:
//
//	go test -race -run='^$' -bench='BenchmarkPutBlob|BenchmarkReadBlob' -benchmem -count=10 ./internal/adapters/s3/
var blobBenchSizes = []struct {
	name string
	size int
}{
	{"4KiB", 4 << 10},
	{"256KiB", 256 << 10},
	{"2MiB", 2 << 20},
	{"64MiB", 64 << 20},
}

func benchPayload(size int) []byte {
	data := make([]byte, size)
	r := rand.New(rand.NewPCG(1, uint64(size)))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	return data
}

// The store builds its own transport, so the fake runs in-process and B/op
// counts its allocations too. Compare B/op between runs of this harness, not
// as an absolute client cost: on PUT the fake buffers each body (about one blob
// size per op); on GET it writes the stored slice without a per-request copy.

// BenchmarkPutBlob reports the wall time, allocations and S3 round trips
// (requests/put) of one content-addressed write, canonical sizes through Put
// and larger ones through PutVectorFile.
func BenchmarkPutBlob(b *testing.B) {
	for _, tc := range blobBenchSizes {
		b.Run(tc.name, func(b *testing.B) {
			fake := newFakeS3(b)
			blobs := fake.store()
			put := blobs.Put
			if int64(tc.size) > 2<<20 {
				put = blobs.PutVectorFile
			}
			data := benchPayload(tc.size)
			ctx := context.Background()
			b.SetBytes(int64(tc.size))
			b.ReportAllocs()
			before := fake.requests.Load()
			for b.Loop() {
				blob, err := put(ctx, "bench-org", data)
				if err != nil {
					b.Fatal(err)
				}
				// Each iteration writes a new object instead of replaying a 412.
				fake.delete(blob.Key)
			}
			b.ReportMetric(float64(fake.requests.Load()-before)/float64(b.N), "requests/put")
		})
	}
}

// BenchmarkReadBlob reports the wall time and allocations (B/op versus the
// blob size) of one verified read.
func BenchmarkReadBlob(b *testing.B) {
	for _, tc := range blobBenchSizes {
		b.Run(tc.name, func(b *testing.B) {
			fake := newFakeS3(b)
			blobs := fake.store()
			read := blobs.Read
			if int64(tc.size) > 2<<20 {
				read = blobs.ReadVectorFile
			}
			data := benchPayload(tc.size)
			blob := content.Blob{Key: "bench/sha256/" + content.Hash(data), SHA256: content.Hash(data), Size: int64(tc.size)}
			fake.set(blob.Key, data)
			ctx := context.Background()
			b.SetBytes(int64(tc.size))
			b.ReportAllocs()
			before := fake.requests.Load()
			for b.Loop() {
				if _, err := read(ctx, blob); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(fake.requests.Load()-before)/float64(b.N), "requests/read")
		})
	}
}
