package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type member struct {
	name    string
	size    int64
	regular bool
	body    io.Reader
	close   func() error
}
type archiveReader struct {
	obj       object
	tar       *tar.Reader
	zip       *zip.Reader
	zipOffset int
	close     func() error
	cancel    context.CancelFunc
	bytes     atomic.Int64
	total     *int64
	finish    func() error
	zipMember func(string) bool
}
type countedReader struct {
	io.Reader
	count *atomic.Int64
}

func (r countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count.Add(int64(n))
	return n, err
}
func openArchive(ctx context.Context, s *storage, obj object) (*archiveReader, error) {
	r := &archiveReader{obj: obj}
	// Retain the streaming HTTP response between invocations. Cancellation of
	// an active page closes it; idle-stream eviction owns its lifetime otherwise.
	live, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopOpening := context.AfterFunc(ctx, cancel)
	defer stopOpening()
	r.cancel = cancel
	if strings.HasSuffix(obj.Key, ".zip") {
		remote := &rangeReader{ctx: live, s: s, obj: obj, count: &r.bytes}
		zr, err := zip.NewReader(remote, obj.Size)
		if err != nil {
			cancel()
			return nil, archiveError(err)
		}
		r.zip = zr
		r.zipMember = s.cfg.members.MatchString
		r.close = func() error { cancel(); s.close(); return nil }
		var total int64
		for _, f := range zr.File {
			if f.Mode().IsRegular() && s.cfg.members.MatchString(f.Name) {
				total++
			}
		}
		r.total = &total
		return r, nil
	}
	body, err := s.open(live, obj, "")
	if err != nil {
		cancel()
		return nil, err
	}
	gz, err := gzip.NewReader(bufio.NewReader(countedReader{body, &r.bytes}))
	if err != nil {
		body.Close()
		cancel()
		return nil, archiveError(err)
	}
	r.tar = tar.NewReader(gz)
	r.finish = func() error { _, err := io.Copy(io.Discard, gz); return err }
	r.close = func() error { cancel(); gz.Close(); err := body.Close(); s.close(); return err }
	return r, nil
}
func (r *archiveReader) next() (member, error) {
	if r.tar != nil {
		h, err := r.tar.Next()
		if err == io.EOF && r.finish != nil {
			if finishErr := r.finish(); finishErr != nil {
				return member{}, finishErr
			}
		}
		if err != nil {
			return member{}, err
		}
		return member{name: h.Name, size: h.Size, regular: h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA, body: r.tar, close: func() error { return nil }}, nil
	}
	if r.zipOffset >= len(r.zip.File) {
		return member{}, io.EOF
	}
	f := r.zip.File[r.zipOffset]
	r.zipOffset++
	if !f.Mode().IsRegular() || !r.zipMember(f.Name) {
		return member{name: f.Name, regular: false, body: strings.NewReader(""), close: func() error { return nil }}, nil
	}
	if f.UncompressedSize64 > uint64(maxMemberBytes) {
		return member{}, quivrplugin.SourceError("member_too_large", "archive member exceeds the upload bound")
	}
	b, err := f.Open()
	if err != nil {
		return member{}, err
	}
	return member{name: f.Name, size: int64(f.UncompressedSize64), regular: true, body: b, close: b.Close}, nil
}

// ReaderAt fetches only requested ZIP ranges; the central directory and each
// member are read without downloading or extracting the entire archive.
type rangeReader struct {
	ctx   context.Context
	s     *storage
	obj   object
	count *atomic.Int64
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative archive offset")
	}
	if off >= r.obj.Size {
		return 0, io.EOF
	}
	nbytes := int64(len(p))
	if off+nbytes > r.obj.Size {
		nbytes = r.obj.Size - off
	}
	body, err := r.s.open(r.ctx, r.obj, fmt.Sprintf("bytes=%d-%d", off, off+nbytes-1))
	if err != nil {
		return 0, err
	}
	defer body.Close()
	n, err := io.ReadFull(body, p[:nbytes])
	r.count.Add(int64(n))
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}
func archiveError(err error) error {
	if err == nil {
		return nil
	}
	var timeout net.Error
	if errors.As(err, &timeout) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrClosedPipe) {
		return quivrplugin.TransientError("storage_unavailable", "source archive read was interrupted")
	}
	if _, ok := err.(*quivrplugin.Error); ok {
		return err
	}
	return quivrplugin.SourceError("malformed_archive", "archive could not be decoded completely")
}
