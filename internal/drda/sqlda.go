package drda

import (
	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
	"strings"
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

// SQLDAVariant selects the SQLDAGRP layout, which differs between Db2
// products beyond what the public DRDA text describes.
type SQLDAVariant int

const (
	// SQLDALUW: Db2 LUW (SQLAM 11). Two extra bytes after the comments
	// pair and a third trailing nullable group.
	SQLDALUW SQLDAVariant = iota
	// SQLDAIBMi: Db2 for i (and, until shown otherwise, z/OS).
	SQLDAIBMi
)

// sqldaVariantFor picks the layout from the server's product id
// ("SQL11058" = LUW, "QSQ07050" = i, "DSN13015" = z/OS).
func sqldaVariantFor(prdid string) SQLDAVariant {
	if strings.HasPrefix(prdid, "SQL") {
		return SQLDALUW
	}
	return SQLDAIBMi
}

// ParseSQLDARD parses a SQLDARD body: an SQLCARD followed by the
// descriptor area. Returns the SQLCA (possibly nil), the columns, and
// any parse error.
func ParseSQLDARD(body []byte, littleEndian bool) (*SQLCA, []ColumnDesc, error) {
	return ParseSQLDARDVariant(body, littleEndian, SQLDALUW, 0)
}

// ParseSQLDARDVariant is ParseSQLDARD with an explicit layout variant
// and the server's single-byte CCSID for names.
func ParseSQLDARDVariant(body []byte, littleEndian bool, variant SQLDAVariant, sbc uint16) (*SQLCA, []ColumnDesc, error) {
	r := &byteReader{b: body, le: littleEndian, sbc: sbc}
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
		if variant == SQLDAIBMi {
			// Db2 for i flows six more (zero) bytes in SQLDHGRP before
			// SQLNUMGRP; observed on V7R5.
			_ = r.take(6)
		}
	}
	n := int(r.u16())
	cols := make([]ColumnDesc, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		cols = append(cols, parseSQLDAGRP(r, variant))
	}
	if r.err != nil {
		return ca, nil, r.err
	}
	return ca, cols, nil
}

// parseSQLDAGRP reads one column description. Observed layouts (the
// public DRDA text does not describe the eight bytes after SQLCCSID):
//
//	SQLPRECISION I2, SQLSCALE I2, SQLLENGTH I8, SQLTYPE I2, SQLCCSID I2 (BE)
//	8 bytes (zero) on Db2 for i, 10 on Db2 LUW
//	SQLDOPTGRP indicator (0xFF = absent; LUW uses 0x08 on character columns)
//	  SQLUNNAMED I2, SQLNAME (VCM,VCS), SQLLABEL (VCM,VCS), SQLCOMMENTS (VCM,VCS)
//	SQLUDTGRP indicator (+ type name, class name pairs when present)
//	SQLDXGRP indicator (+ key/updatable/generated/parmmode, rdbnam, 4 pairs)
//	LUW only: one more nullable group indicator
func parseSQLDAGRP(r *byteReader, variant SQLDAVariant) ColumnDesc {
	var c ColumnDesc
	c.Precision = int32(int16(r.u16()))
	c.Scale = int32(int16(r.u16()))
	c.Length = int64(r.u64())
	c.SQLType = r.u16()
	c.CCSID = r.u16BE() // always big-endian
	if variant == SQLDALUW {
		_ = r.take(10)
	} else {
		_ = r.take(8)
	}
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
		if variant == SQLDALUW {
			// Third trailing group (observed null).
			if r.remaining() > 0 && r.b[r.pos] == 0xFF {
				r.pos++
			}
		}
	}
	return c
}

func decodeEBCDICText(b []byte) string { return ddm.DecodeEBCDIC(b) }
