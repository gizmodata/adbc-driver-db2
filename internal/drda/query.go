package drda

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
)

// Query is an open DRDA cursor. Rows are pulled one server block at a
// time (QRYBLKSZ bytes per CNTQRY round trip), so memory is bounded by
// the block size rather than the result-set size.
type Query struct {
	conn    *Conn
	Columns []ColumnDesc
	Fields  []FieldDesc
	decoder RowDecoder

	corr     uint16 // correlation id of the OPNQRY; CNTQRY must reuse it
	qryinsid uint64
	opened   bool
	done     bool
	closed   bool

	pending []([]Value)
	extdta  [][]byte
	// partial holds the tail of a row split across query blocks.
	partial []byte
	// Warnings collects non-error SQLCAs (e.g. +100 at end of data).
	Warnings []*SQLCA
	// Result is populated instead of rows when the statement turned out
	// not to produce a result set (INSERT/UPDATE/DDL run via Query).
	Result *Result
}

// IsResultSet reports whether the statement produced a cursor.
func (q *Query) IsResultSet() bool { return q.Result == nil }

// Query prepares sql and opens a cursor over its result. If the
// statement produces no result set it is executed instead and
// q.Result is set.
func (c *Conn) Query(ctx context.Context, sql string) (*Query, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, err := c.queryLocked(ctx, sql)
	if err != nil && c.autoBind(ctx, err) {
		q, err = c.queryLocked(ctx, sql)
	}
	return q, err
}

func (c *Conn) queryLocked(ctx context.Context, sql string) (*Query, error) {
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return nil, err
	}
	q := c.newQuery()
	c.trace("sql: %s", sql)

	if looksLikeQuery(sql) {
		// One round trip: PRPSQLSTT + SQLSTT + OPNQRY chained.
		c.send(ctx, c.packPRPSQLSTT(c.pkgSN), 1, true, false)
		c.send(ctx, c.packSQLSTT(sql), 1, false, false)
		c.send(ctx, c.packOPNQRY(c.pkgSN, false), 2, false, true)
		if err := c.flush(ctx); err != nil {
			return nil, err
		}
		replies, err := c.readChain(ctx, 2)
		if err != nil {
			return nil, err
		}
		if err := q.consume(replies); err != nil {
			return nil, err
		}
		if !q.opened && q.Result == nil {
			return nil, fmt.Errorf("drda: OPNQRY produced neither a cursor nor an error")
		}
		c.openQuery = q
		return q, nil
	}

	// Prepare first, then decide between OPNQRY and EXCSQLSTT.
	c.send(ctx, c.packPRPSQLSTT(c.pkgSN), 1, true, false)
	c.send(ctx, c.packSQLSTT(sql), 1, false, true)
	if err := c.flush(ctx); err != nil {
		return nil, err
	}
	replies, err := c.readChain(ctx, 1)
	if err != nil {
		return nil, err
	}
	if err := q.consume(replies); err != nil {
		return nil, err
	}
	if len(q.Columns) > 0 {
		c.send(ctx, c.packOPNQRY(c.pkgSN, false), 2, false, true)
		if err := c.flush(ctx); err != nil {
			return nil, err
		}
		replies, err = c.readChain(ctx, 2)
		if err != nil {
			return nil, err
		}
		if err := q.consume(replies); err != nil {
			return nil, err
		}
		c.openQuery = q
		return q, nil
	}
	c.send(ctx, c.packEXCSQLSTT(c.pkgSN), 2, false, true)
	if err := c.flush(ctx); err != nil {
		return nil, err
	}
	replies, err = c.readChain(ctx, 2)
	if err != nil {
		return nil, err
	}
	res, err := c.collectResult(replies)
	if err != nil {
		return nil, err
	}
	q.Result = res
	q.done = true
	return q, nil
}

func looksLikeQuery(sql string) bool {
	s := strings.TrimSpace(sql)
	// Skip leading comments.
	for strings.HasPrefix(s, "--") {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return false
		}
		s = strings.TrimSpace(s[i+1:])
	}
	if len(s) < 4 {
		return false
	}
	head := strings.ToUpper(s[:min(len(s), 6)])
	return strings.HasPrefix(head, "SELECT") || strings.HasPrefix(head, "WITH") ||
		strings.HasPrefix(head, "VALUES") || strings.HasPrefix(head, "(")
}

// consume folds one transmission's replies into the query state.
func (q *Query) consume(replies []*ddm.DSS) error {
	c := q.conn
	var firstErr error
	for _, d := range replies {
		switch d.CodePoint {
		case ddm.SQLDARD:
			ca, cols, err := ParseSQLDARD(d.Payload, c.Server.LittleEndian)
			if err != nil {
				return err
			}
			if ca.IsError() {
				firstErr = preferSQLCA(firstErr, ca)
				continue
			}
			if ca.IsWarning() {
				q.Warnings = append(q.Warnings, ca)
			}
			if q.Columns == nil {
				q.Columns = cols
				names := make([]string, 0, len(cols))
				for i, col := range cols {
					if i < 12 {
						names = append(names, col.Name)
					}
				}
				c.trace("SQLDARD: %d columns (%v ...)", len(cols), names)
			}
		case ddm.OPNQRYRM:
			q.opened = true
			q.corr = d.CorrelationID
			p, err := ddm.ParseParams(d.Payload)
			if err == nil {
				q.qryinsid, _ = p.Uint64(ddm.QRYINSID)
			}
		case ddm.QRYDSC:
			fields, err := ParseQRYDSC(d.Payload)
			if err != nil {
				return err
			}
			q.Fields = fields
			q.decoder.Fields = fields
			c.trace("QRYDSC: %d fields", len(fields))
		case ddm.QRYDTA:
			if q.Fields == nil {
				return fmt.Errorf("drda: QRYDTA before QRYDSC")
			}
			payload := d.Payload
			if len(q.partial) > 0 {
				payload = append(q.partial, payload...)
				q.partial = nil
			}
			leftover, ca, err := q.decoder.DecodeBlock(payload, func(row []Value) error {
				q.pending = append(q.pending, row)
				return nil
			}, func(w *SQLCA) {
				if len(q.Warnings) < 100 {
					q.Warnings = append(q.Warnings, w)
				}
			})
			if err != nil {
				return err
			}
			if len(leftover) > 0 {
				q.partial = append([]byte(nil), leftover...)
			}
			c.trace("QRYDTA: %d bytes -> %d rows so far, %d bytes carried over", len(payload), len(q.pending), len(leftover))
			if ca != nil {
				if ca.IsError() {
					if firstErr == nil {
						firstErr = ca
					}
				} else {
					q.Warnings = append(q.Warnings, ca)
				}
				if ca.SQLCode == 100 {
					q.done = true
				}
			}
		case ddm.EXTDTA:
			q.extdta = append(q.extdta, d.Payload)
		case ddm.ENDQRYRM:
			q.done = true
			if len(q.partial) > 0 {
				return fmt.Errorf("drda: query ended with %d undecoded bytes of row data (row decoder out of sync)", len(q.partial))
			}
		case ddm.SQLCARD:
			ca, err := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if err != nil {
				return err
			}
			if ca == nil {
				continue
			}
			if ca.IsError() {
				firstErr = preferSQLCA(firstErr, ca)
			} else if ca.SQLCode != 0 {
				q.Warnings = append(q.Warnings, ca)
				if ca.SQLCode == 100 {
					q.done = true
				}
			}
		case ddm.SQLERRRM, ddm.ENDUOWRM:
		default:
			if err := c.replyError(d); err != nil {
				firstErr = preferSQLCA(firstErr, err)
			}
		}
	}
	if firstErr != nil {
		q.done = true
		return firstErr
	}
	q.resolveLOBs()
	return nil
}

// preferSQLCA keeps an SQLCA error over a generic reply-message error
// (e.g. OPNQFLRM), since the SQLCA carries the SQLCODE and message text.
func preferSQLCA(current, candidate error) error {
	if current == nil {
		return candidate
	}
	var ca *SQLCA
	if errors.As(candidate, &ca) && !errors.As(current, &ca) {
		return candidate
	}
	return current
}

// resolveLOBs substitutes EXTDTA payloads for LobRef placeholders in
// the pending rows, in row-major order.
func (q *Query) resolveLOBs() {
	if len(q.extdta) == 0 {
		return
	}
	idx := 0
	for _, row := range q.pending {
		for i, v := range row {
			ref, ok := v.(LobRef)
			if !ok {
				continue
			}
			if idx >= len(q.extdta) {
				row[i] = nil
				continue
			}
			data := q.extdta[idx]
			idx++
			f := q.Fields[i]
			base := f.Type &^ 1
			if base == TypeLobBytes || base == TypeLobCSBCS {
				// Inline LOB EXTDTA starts with a status byte (0x00 = ok).
				if len(data) > 0 {
					data = data[1:]
				}
			}
			if ref.IsChar {
				row[i] = decodeMixed(data)
			} else {
				row[i] = cloneBytes(data)
			}
		}
	}
	q.extdta = q.extdta[:0]
}

// Next returns the next batch of rows, fetching another block from the
// server when the local buffer is empty. Returns nil, nil at end.
func (q *Query) Next(ctx context.Context) ([][]Value, error) {
	if len(q.pending) > 0 {
		rows := q.pending
		q.pending = nil
		return rows, nil
	}
	if q.done || q.closed {
		return nil, nil
	}
	c := q.conn
	c.mu.Lock()
	defer c.mu.Unlock()
	// A block without rows and without ENDQRYRM is unusual; ask again a
	// few times (some servers answer the first CNTQRY with metadata only)
	// before treating it as the end.
	for attempt := 0; attempt < 3; attempt++ {
		c.send(ctx, c.packCNTQRY(c.pkgSN, q.qryinsid), q.corr, false, true)
		if err := c.flush(ctx); err != nil {
			return nil, err
		}
		replies, err := c.readChain(ctx, q.corr)
		if err != nil {
			return nil, err
		}
		if err := q.consume(replies); err != nil {
			return nil, err
		}
		if len(q.pending) > 0 || q.done {
			break
		}
		c.trace("CNTQRY returned no rows and no ENDQRYRM (attempt %d)", attempt+1)
	}
	rows := q.pending
	q.pending = nil
	if len(rows) == 0 && !q.done {
		c.trace("giving up on query after empty CNTQRY replies")
		q.done = true
	}
	return rows, nil
}

// Close releases the cursor. If the server has not already ended the
// query (ENDQRYRM), a CLSQRY is sent.
func (q *Query) Close(ctx context.Context) error {
	q.conn.mu.Lock()
	defer q.conn.mu.Unlock()
	return q.closeLocked(ctx)
}

func (q *Query) closeLocked(ctx context.Context) error {
	if q.closed {
		return nil
	}
	q.closed = true
	q.pending = nil
	if q.conn.openQuery == q {
		q.conn.openQuery = nil
	}
	if q.done || !q.opened {
		return nil
	}
	c := q.conn
	c.send(ctx, c.packCLSQRY(c.pkgSN, q.qryinsid), q.corr, false, true)
	if err := c.flush(ctx); err != nil {
		return err
	}
	replies, err := c.readChain(ctx, q.corr)
	if err != nil {
		return err
	}
	q.done = true
	for _, d := range replies {
		if d.CodePoint == ddm.SQLCARD {
			ca, perr := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if perr == nil && ca.IsError() {
				return ca
			}
			continue
		}
		if err := c.replyError(d); err != nil {
			return err
		}
	}
	return nil
}

// newQuery creates a Query configured for this server's typedef
// (endianness and character CCSIDs).
func (c *Conn) newQuery() *Query {
	q := &Query{conn: c}
	q.decoder.LittleEndian = c.Server.LittleEndian
	q.decoder.CCSIDSBC = c.Server.CCSIDSBC
	q.decoder.CCSIDDBC = c.Server.CCSIDDBC
	q.decoder.CCSIDMBC = c.Server.CCSIDMBC
	return q
}
