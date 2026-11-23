package codextranscript

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// errAfter reads r, then fails with err.
type errAfter struct {
	r   io.Reader
	err error
}

func (e errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		err = e.err
	}
	return n, err
}

// A stream that fails part way through the first record is a failed stream,
// not a record that does not decode.
func TestDecodeFirstReportsAFailedStream(t *testing.T) {
	failure := errors.Join(ErrDecompress, errors.New("boom"))
	_, err := DecodeFirst(errAfter{r: strings.NewReader(`{"type":"session_meta","pay`), err: failure})
	if !errors.Is(err, ErrDecompress) {
		t.Fatalf("error %v, want ErrDecompress", err)
	}
}

// A frame whose size exceeds the decoder's memory bound is unreadable, and
// the decoder it used still serves the next rollout.
func TestOversizedCompressedFrameIsUnreadable(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithWindowSize(2*maxDecoderWindow))
	if err != nil {
		t.Fatal(err)
	}
	big := enc.EncodeAll(make([]byte, maxDecoderWindow+1<<20), nil)
	r, err := decompress(bytes.NewReader(big))
	if err == nil {
		_, err = io.ReadAll(r)
		_ = r.Close()
	}
	if !errors.Is(err, ErrDecompress) {
		t.Fatalf("error %v, want ErrDecompress", err)
	}
	small := []byte("{}\n")
	r, err = decompress(bytes.NewReader(zstdBytes(t, small)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, small) {
		t.Fatalf("after the failure: %q, %v", got, err)
	}
}

// Whole reads see a stream that fails part way as a file cut off there.
// An error reading the compressed file is not a corrupt stream: it is
// returned as it is, never cut to an end of data that would be cached.
func TestDecompressReturnsReadErrors(t *testing.T) {
	data := make([]byte, 1<<20)
	rand.New(rand.NewSource(2)).Read(data)
	good := zstdBytes(t, data)
	failed := errors.New("input/output error")
	for name, open := range map[string]func(io.Reader) (io.ReadCloser, error){
		"strict": decompress, "whole": decompressWhole,
	} {
		r, err := open(errAfter{r: bytes.NewReader(good[:len(good)/2]), err: failed})
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(r)
		_ = r.Close()
		if !errors.Is(err, failed) || errors.Is(err, ErrDecompress) {
			t.Fatalf("%s: err = %v, want the read error itself", name, err)
		}
	}
}

func TestDecompressEndsAtAFailedStream(t *testing.T) {
	data := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(data)
	good := zstdBytes(t, data)
	r, err := Decompress(bytes.NewReader(good[:len(good)/2]))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || len(got) == 0 || len(got) >= len(data) || !bytes.Equal(got, data[:len(got)]) {
		t.Fatalf("read %d of %d bytes, %v", len(got), len(data), err)
	}
}
