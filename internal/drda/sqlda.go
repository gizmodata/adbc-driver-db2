package drda

import (
	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
)

// ColumnDesc describes one result column or one statement parameter as
// reported by SQLDARD.
type ColumnDesc struct {
	Name      string
	Label     string
	SQLType   uint16 // Db2 SQLTYPE (odd = nullable)
	Length    int64
	Precision int32
	Scale     int32
	CCSID     uint16
	// Extended (SQLDXGRP) metadata, when supplied.
	BaseTable  string
	BaseSchema string
	BaseColumn string
	Updatable  bool
	Generated  bool
}

// Nullable reports whether the column allows NULLs.
func (c ColumnDesc) Nullable() bool { return SQLTypeNullable(c.SQLType) }

// Base returns the SQLTYPE with the nullable bit cleared.
func (c ColumnDesc) Base() uint16 { return BaseSQLType(c.SQLType) }

// ParseSQLDARD parses a SQLDARD body: an SQLCARD followed by the
// descriptor area. Returns the SQLCA (possibly nil), the columns, and
// any parse error.
func ParseSQLDARD(body []byte, littleEndian bool) (*SQLCA, []ColumnDesc, error) {
	r := &byteReader{b: body, le: littleEndian}
	ca := parseSQLCAGRP(r)
	if r.err != nil {
		return nil, nil, r.err
	}
	if ca.IsError() {
		return ca, nil, nil
	}
	// SQLDHGRP
	if r.u8() != 0xFF {
		_ = r.u16() // SQLDHOLD
		_ = r.u16() // SQLDRETURN
		_ = r.u16() // SQLDSCROLL
		_ = r.u16() // SQLDSENSITIVE
		_ = r.u16() // SQLDFCODE
		_ = r.u16() // SQLDKEYTYPE
		_ = r.vcs() // SQLDRDBNAM
		_ = r.vcmOrVcs()
	}
	n := int(r.u16())
	cols := make([]ColumnDesc, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		cols = append(cols, parseSQLDAGRP(r))
	}
	if r.err != nil {
		return ca, nil, r.err
	}
	return ca, cols, nil
}

func parseSQLDAGRP(r *byteReader) ColumnDesc {
	var c ColumnDesc
	c.Precision = int32(int16(r.u16()))
	c.Scale = int32(int16(r.u16()))
	c.Length = int64(r.u64())
	c.SQLType = r.u16()
	c.CCSID = r.u16BE() // always big-endian
	// Db2 LUW (SQLAM 11) flows ten bytes here that neither Derby nor
	// the public DRDA V5 text names (an I8 and two I1s; the ninth byte
	// is 0x08 for character columns). Observed otherwise zero.
	_ = r.take(10)
	// SQLDOPTGRP
	if r.u8() != 0xFF {
		_ = r.u16() // SQLUNNAMED (1 = expression column; SQLNAME is then the ordinal)
		c.Name = r.vcmOrVcs()
		c.Label = r.vcmOrVcs()
		_ = r.vcmOrVcs() // SQLCOMMENTS
		if c.Name == "" {
			c.Name = c.Label
		}
		// SQLUDTGRP
		if r.u8() != 0xFF {
			_ = r.vcmOrVcs() // type name
			_ = r.vcmOrVcs() // class name
		}
		// SQLDXGRP
		if r.u8() != 0xFF {
			_ = r.u16()                 // SQLXKEYMEM
			c.Updatable = r.u16() != 0  // SQLXUPDATEABLE
			c.Generated = r.u16() != 0  // SQLXGENERATED
			_ = r.u16()                 // SQLXPARMMODE
			_ = r.vcs()                 // SQLXRDBNAM
			_ = r.vcmOrVcs()            // SQLXCORNAME
			c.BaseTable = r.vcmOrVcs()  // SQLXBASENAME
			c.BaseSchema = r.vcmOrVcs() // SQLXSCHEMA
			c.BaseColumn = r.vcmOrVcs() // SQLXNAME
		}
		// Third trailing group (observed null).
		if r.remaining() > 0 && r.b[r.pos] == 0xFF {
			r.pos++
		}
	}
	return c
}

func decodeEBCDICText(b []byte) string { return ddm.DecodeEBCDIC(b) }
