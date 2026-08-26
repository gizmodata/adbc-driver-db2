package db2

import (
	"regexp"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// Db2 requires a FROM clause; many ADBC consumers probe with a bare
// "SELECT 1". A SELECT with no FROM anywhere is given SYSIBM.SYSDUMMY1.
var (
	reSelectStart = regexp.MustCompile(`(?is)^\s*select\b`)
	reFromWord    = regexp.MustCompile(`(?is)\bfrom\b`)
)

func addDummyFrom(sql string) string {
	if !reSelectStart.MatchString(sql) || reFromWord.MatchString(sql) {
		return sql
	}
	trimmed := strings.TrimRight(sql, " \t\r\n;")
	return trimmed + " FROM SYSIBM.SYSDUMMY1"
}

// castParamMarkers replaces each top-level "?" (outside quotes and
// comments) with CAST(? AS <db2 type>) using the bound Arrow schema, so
// Db2 can type parameter markers that appear in a select list or in
// expressions (SQL0418N otherwise).
func castParamMarkers(sql string, schema *arrow.Schema) string {
	var b strings.Builder
	b.Grow(len(sql) + 32*schema.NumFields())
	idx := 0
	inStr, inIdent, lineComment, blockComment := false, false, false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case lineComment:
			b.WriteByte(c)
			if c == '\n' {
				lineComment = false
			}
		case blockComment:
			b.WriteByte(c)
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				b.WriteByte('/')
				i++
				blockComment = false
			}
		case inStr:
			b.WriteByte(c)
			if c == '\'' {
				inStr = false
			}
		case inIdent:
			b.WriteByte(c)
			if c == '"' {
				inIdent = false
			}
		case c == '\'':
			inStr = true
			b.WriteByte(c)
		case c == '"':
			inIdent = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			lineComment = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			blockComment = true
			b.WriteByte(c)
		case c == '?':
			if idx < schema.NumFields() {
				typ, err := db2TypeForArrow(schema.Field(idx), maxVarcharLength)
				if err == nil {
					b.WriteString("CAST(? AS ")
					b.WriteString(typ)
					b.WriteByte(')')
				} else {
					b.WriteByte(c)
				}
			} else {
				b.WriteByte(c)
			}
			idx++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
