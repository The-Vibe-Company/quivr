package main

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const maxZipDirectoryBytes = 4 << 20
const maxZipEntries = 100000

// Bound both the declared allocation and the actual central directory before
// archive/zip allocates per-file metadata. ZIP64 uses the same bounds.
func checkZipDirectory(r io.ReaderAt, size int64) error {
	malformed := func() error { return quivrplugin.SourceError("malformed_archive", "invalid ZIP directory") }
	limited := func() error {
		return quivrplugin.SourceError("zip_directory_too_large", "ZIP directory exceeds 4 MiB or 100000 entries; use tar.gz for larger member sets")
	}
	if size < 22 {
		return malformed()
	}
	tail := make([]byte, min(size, int64(65535+22)))
	if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil {
		return err
	}
	var end []byte
	var endOffset int64
	for i := len(tail) - 22; i >= 0; i-- {
		if bytes.Equal(tail[i:i+4], []byte{'P', 'K', 5, 6}) && i+22+int(binary.LittleEndian.Uint16(tail[i+20:i+22])) <= len(tail) {
			end = tail[i:]
			endOffset = size - int64(len(tail)) + int64(i)
			break
		}
	}
	if end == nil || binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 {
		return malformed()
	}
	records := uint64(binary.LittleEndian.Uint16(end[10:12]))
	directorySize := uint64(binary.LittleEndian.Uint32(end[12:16]))
	directoryOffset := uint64(binary.LittleEndian.Uint32(end[16:20]))
	if records == 0xffff || directorySize == 0xffffffff || directoryOffset == 0xffffffff {
		if endOffset < 20 {
			return malformed()
		}
		locator := make([]byte, 20)
		if _, err := r.ReadAt(locator, endOffset-20); err != nil {
			return err
		}
		if !bytes.Equal(locator[:4], []byte{'P', 'K', 6, 7}) || binary.LittleEndian.Uint32(locator[4:8]) != 0 || binary.LittleEndian.Uint32(locator[16:20]) != 1 {
			return malformed()
		}
		offset := binary.LittleEndian.Uint64(locator[8:16])
		if offset > uint64(endOffset-20) || uint64(endOffset-20)-offset < 56 {
			return malformed()
		}
		header := make([]byte, 56)
		if _, err := r.ReadAt(header, int64(offset)); err != nil {
			return err
		}
		if !bytes.Equal(header[:4], []byte{'P', 'K', 6, 6}) || binary.LittleEndian.Uint64(header[4:12]) < 44 || binary.LittleEndian.Uint32(header[16:20]) != 0 || binary.LittleEndian.Uint32(header[20:24]) != 0 {
			return malformed()
		}
		records = binary.LittleEndian.Uint64(header[32:40])
		directorySize = binary.LittleEndian.Uint64(header[40:48])
		directoryOffset = binary.LittleEndian.Uint64(header[48:56])
		endOffset = int64(offset)
	}
	if records > maxZipEntries || directorySize > maxZipDirectoryBytes {
		return limited()
	}
	// Strict framing also prevents archive/zip's permissive base-offset fallback
	// from reading a different, unbounded directory than the one checked here.
	if directoryOffset > uint64(endOffset) || directorySize != uint64(endOffset)-directoryOffset {
		return malformed()
	}
	directory := make([]byte, directorySize)
	if _, err := r.ReadAt(directory, int64(directoryOffset)); err != nil {
		return err
	}
	var count uint64
	for len(directory) > 0 {
		if len(directory) < 46 || !bytes.Equal(directory[:4], []byte{'P', 'K', 1, 2}) {
			return malformed()
		}
		length := 46 + int(binary.LittleEndian.Uint16(directory[28:30])) + int(binary.LittleEndian.Uint16(directory[30:32])) + int(binary.LittleEndian.Uint16(directory[32:34]))
		if length > len(directory) {
			return malformed()
		}
		directory = directory[length:]
		count++
		if count > maxZipEntries {
			return limited()
		}
	}
	if count != records {
		return malformed()
	}
	return nil
}
