package db2

import (
	"context"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// streamingRecordReader converts DRDA query blocks into Arrow records
// on demand. Each Next() pulls at most batchSize rows (and, when
// batchBytes > 0, stops early once the rows taken are estimated to reach
// batchBytes), fetching further query blocks from the server (CNTQRY) as
// needed, so peak memory is bounded by the query block size + one Arrow
// batch regardless of the result-set size.
type streamingRecordReader struct {
	ctx       context.Context
	q         *drda.Query
	conn      *connectionImpl
	schema    *arrow.Schema
	alloc     memory.Allocator
	batchSize int
	// batchBytes caps the estimated decoded size of one batch (0 = off).
	batchBytes int64
	current    arrow.Record
	buffered   [][]drda.Value
	// bufferedBytes is the estimated size of the rows in buffered.
	bufferedBytes int64
	err           error
	refs          atomic.Int64
	done          bool
	closed        bool
}

func newStreamingRecordReader(ctx context.Context, conn *connectionImpl, q *drda.Query, batchSize int) *streamingRecordReader {
	r := &streamingRecordReader{
		ctx:        ctx,
		q:          q,
		conn:       conn,
		schema:     schemaFor(q.Columns),
		alloc:      conn.alloc,
		batchSize:  batchSize,
		batchBytes: conn.cfg.batchBytes,
	}
	r.refs.Store(1)
	return r
}

func (r *streamingRecordReader) Retain() { r.refs.Add(1) }

func (r *streamingRecordReader) Release() {
	if r.refs.Add(-1) != 0 {
		return
	}
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	r.close()
}

func (r *streamingRecordReader) close() {
	if r.closed {
		return
	}
	r.closed = true
	_ = r.q.Close(context.Background())
	r.conn.queryFinished(r.err == nil)
}

func (r *streamingRecordReader) Schema() *arrow.Schema { return r.schema }

func (r *streamingRecordReader) RecordBatch() arrow.RecordBatch { return r.current }

// Record is the deprecated alias for RecordBatch.
func (r *streamingRecordReader) Record() arrow.RecordBatch { return r.current }

func (r *streamingRecordReader) Err() error { return r.err }

func (r *streamingRecordReader) Next() bool {
	if r.err != nil || r.done || r.closed {
		return false
	}
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	rows := r.takeRows()
	if rows == nil {
		if r.err == nil {
			r.done = true
			r.close()
		}
		return false
	}
	rec, err := recordFromRows(r.alloc, r.schema, rows)
	if err != nil {
		r.err = err
		return false
	}
	r.current = rec
	return true
}

// takeRows returns up to batchSize rows (fewer once batchBytes is
// reached), pulling from the server as needed. nil means end of data (or
// error, see r.err).
func (r *streamingRecordReader) takeRows() [][]drda.Value {
	for len(r.buffered) < r.batchSize && (r.batchBytes == 0 || r.bufferedBytes < r.batchBytes) {
		more, err := r.q.Next(r.ctx)
		if err != nil {
			r.err = fromDRDAError(err)
			return nil
		}
		if more == nil {
			break
		}
		if r.batchBytes > 0 {
			for _, row := range more {
				r.bufferedBytes += rowBytes(row)
			}
		}
		if r.buffered == nil {
			r.buffered = more
		} else {
			r.buffered = append(r.buffered, more...)
		}
	}
	if len(r.buffered) == 0 {
		return nil
	}
	n := len(r.buffered)
	if n > r.batchSize {
		n = r.batchSize
	}
	var taken int64
	if r.batchBytes > 0 {
		// Always take at least one row so a single oversized row still
		// makes progress.
		cut := 1
		taken = rowBytes(r.buffered[0])
		for cut < n && taken < r.batchBytes {
			taken += rowBytes(r.buffered[cut])
			cut++
		}
		n = cut
	}
	out := r.buffered[:n:n]
	r.buffered = r.buffered[n:]
	if len(r.buffered) == 0 {
		r.buffered = nil
		r.bufferedBytes = 0
	} else {
		r.bufferedBytes -= taken
	}
	return out
}

// rowBytes estimates the Arrow-side size of one decoded row. It only
// needs to be proportional to the real encoded size (it drives the
// batch_bytes cut-off), so fixed-width values count at their width and
// variable-width values at their payload length plus an offset.
func rowBytes(row []drda.Value) int64 {
	var n int64
	for _, v := range row {
		switch x := v.(type) {
		case nil:
			n++
		case bool:
			n++
		case int16:
			n += 2
		case int32, float32, drda.Date, drda.Time:
			n += 4
		case int64, float64:
			n += 8
		case drda.Timestamp, drda.Decimal:
			n += 16
		case string:
			n += int64(len(x)) + 4
		case []byte:
			n += int64(len(x)) + 4
		default:
			n += 16
		}
	}
	return n
}

var _ array.RecordReader = (*streamingRecordReader)(nil)
