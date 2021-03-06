// Copyright 2015 Ka-Hing Cheung
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package internal

import (
	"bytes"
	"io"
	"sync"
	"time"

	. "gopkg.in/check.v1"
)

type BufferTest struct {
}

var _ = Suite(&BufferTest{})

type SeqReader struct {
	cur int64
}

func (r *SeqReader) Read(p []byte) (n int, err error) {
	n = len(p)
	for i := range p {
		r.cur++
		p[i] = byte(r.cur)
	}

	return
}

func (r *SeqReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		r.cur = offset
	case 1:
		r.cur += offset
	default:
		panic("unsupported whence")
	}

	return r.cur, nil

}

type SlowReader struct {
	r     io.Reader
	sleep time.Duration
}

func (r *SlowReader) Read(p []byte) (n int, err error) {
	time.Sleep(r.sleep)
	return r.r.Read(p[:MaxInt(len(p), 1337)])
}

func (r *SlowReader) Close() error {
	if reader, ok := r.r.(io.ReadCloser); ok {
		return reader.Close()
	}
	return nil
}

func CompareReader(r1, r2 io.Reader) (int, error) {
	var buf1 [1337]byte
	var buf2 [1337]byte

	for {
		nread, err := r1.Read(buf1[:])
		if err != nil {
			return -1, err
		}

		if nread == 0 {
			break
		}

		nread2, err := io.ReadFull(r2, buf2[:nread])
		if err != nil {
			return -1, err
		}

		if bytes.Compare(buf1[:], buf2[:]) != 0 {
			// fallback to slow path to find the exact point of divergent
			for i, b := range buf1 {
				if buf2[i] != b {
					return i, nil
				}
			}

			if nread2 > nread {
				return nread, nil
			}
		}
	}

	return -1, nil
}

func (s *BufferTest) TestMBuf(t *C) {
	pool := NewBufferPool(1000*1024*1024, 200*1024*1024)
	h := pool.NewPoolHandle()

	n := uint64(2 * BUF_SIZE)
	mb := MBuf{}.Init(h, n)
	t.Assert(len(mb.buffers), Equals, 2)

	r := io.LimitReader(&SeqReader{}, int64(n))

	for {
		nread, err := mb.WriteFrom(r)
		t.Assert(err, IsNil)
		if nread == 0 {
			break
		}
	}
	t.Assert(mb.wbuf, Equals, 1)
	t.Assert(mb.wp, Equals, BUF_SIZE)

	diff, err := CompareReader(mb, io.LimitReader(&SeqReader{}, int64(n)))
	t.Assert(err, IsNil)
	t.Assert(diff, Equals, -1)

	t.Assert(mb.rbuf, Equals, 1)
	t.Assert(mb.rp, Equals, BUF_SIZE)

	t.Assert(h.inUseBuffers, Equals, uint64(2))
	mb.Free()
	t.Assert(h.inUseBuffers, Equals, uint64(0))
}

func (s *BufferTest) TestBuffer(t *C) {
	pool := NewBufferPool(1000*1024*1024, 200*1024*1024)
	h := pool.NewPoolHandle()

	n := uint64(2 * BUF_SIZE)
	mb := MBuf{}.Init(h, n)
	t.Assert(len(mb.buffers), Equals, 2)

	r := func() (io.ReadCloser, error) {
		return &SlowReader{io.LimitReader(&SeqReader{}, int64(n)), 100 * time.Millisecond}, nil
	}

	b := Buffer{}.Init(mb, r)

	diff, err := CompareReader(b, io.LimitReader(&SeqReader{}, int64(n)))
	t.Assert(err, IsNil)
	t.Assert(diff, Equals, -1)
	t.Assert(b.buf, IsNil)
	t.Assert(b.reader, NotNil)
	t.Assert(h.inUseBuffers, Equals, uint64(0))
}

func (s *BufferTest) TestPool(t *C) {
	const MAX = 8
	pool := BufferPool{maxBuffersGlobal: MAX}.Init()
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			var inner sync.WaitGroup
			for j := 0; j < 30; j++ {
				inner.Add(1)
				buf := pool.requestBuffer()
				go func() {
					time.Sleep(1000 * time.Millisecond)
					pool.freeBuffer(buf)
					inner.Done()
				}()
				inner.Wait()
			}
			wg.Done()
		}()
	}

	wg.Wait()
}
