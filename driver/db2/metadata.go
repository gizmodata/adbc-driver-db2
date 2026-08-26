package db2

import (
	"context"
	"runtime/debug"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// queryAll runs sql and materializes every row (for small metadata
// queries only). The unit of work is committed afterwards when
// autocommit is on so catalog reads don't pin locks.
func (c *connectionImpl) queryAll(ctx context.Context, sql string) ([]drda.ColumnDesc, [][]drda.Value, error) {
	q, err := c.conn.Query(ctx, sql)
	if err != nil {
		return nil, nil, fromDRDAError(err)
	}
	if !q.IsResultSet() {
		return nil, nil, nil
	}
	var rows [][]drda.Value
	for {
		batch, err := q.Next(ctx)
		if err != nil {
			_ = q.Close(ctx)
			return nil, nil, fromDRDAError(err)
		}
		if batch == nil {
			break
		}
		rows = append(rows, batch...)
	}
	if err := q.Close(ctx); err != nil {
		return nil, nil, fromDRDAError(err)
	}
	if err := c.autoCommitIfNeeded(ctx); err != nil {
		return nil, nil, err
	}
	return q.Columns, rows, nil
}

func (c *connectionImpl) currentSchema(ctx context.Context) (string, error) {
	_, rows, err := c.queryAll(ctx, "SELECT CURRENT SCHEMA FROM SYSIBM.SYSDUMMY1")
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.TrimSpace(asString(rows[0][0])), nil
}

func asString(v drda.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(fmtValue(v)), "\""))
}

func fmtValue(v drda.Value) string {
	switch x := v.(type) {
	case drda.Decimal:
		return x.String()
	}
	b, _ := toInt64(v)
	return itoa(b)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ---- GetInfo ----

var supportedInfoCodes = []adbc.InfoCode{
	adbc.InfoVendorName,
	adbc.InfoVendorVersion,
	adbc.InfoVendorSql,
	adbc.InfoVendorSubstrait,
	adbc.InfoDriverName,
	adbc.InfoDriverVersion,
	adbc.InfoDriverArrowVersion,
	adbc.InfoDriverADBCVersion,
}

func driverVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return Version
}

func (c *connectionImpl) getInfoImpl(ctx context.Context, infoCodes []adbc.InfoCode) (array.RecordReader, error) {
	if len(infoCodes) == 0 {
		infoCodes = supportedInfoCodes
	}
	bldr := array.NewRecordBuilder(c.alloc, adbc.GetInfoSchema)
	defer bldr.Release()
	nameBldr := bldr.Field(0).(*array.Uint32Builder)
	valueBldr := bldr.Field(1).(*array.DenseUnionBuilder)
	strBldr := valueBldr.Child(int(adbc.InfoValueStringType)).(*array.StringBuilder)
	int64Bldr := valueBldr.Child(int(adbc.InfoValueInt64Type)).(*array.Int64Builder)
	boolBldr := valueBldr.Child(int(adbc.InfoValueBooleanType)).(*array.BooleanBuilder)

	for _, code := range infoCodes {
		nameBldr.Append(uint32(code))
		switch code {
		case adbc.InfoVendorName:
			valueBldr.Append(int8(adbc.InfoValueStringType))
			strBldr.Append(VendorName)
		case adbc.InfoVendorVersion:
			valueBldr.Append(int8(adbc.InfoValueStringType))
			strBldr.Append(c.conn.Server.ProductID)
		case adbc.InfoVendorSql:
			valueBldr.Append(int8(adbc.InfoValueBooleanType))
			boolBldr.Append(true)
		case adbc.InfoVendorSubstrait:
			valueBldr.Append(int8(adbc.InfoValueBooleanType))
			boolBldr.Append(false)
		case adbc.InfoDriverName:
			valueBldr.Append(int8(adbc.InfoValueStringType))
			strBldr.Append(DriverName)
		case adbc.InfoDriverVersion:
			valueBldr.Append(int8(adbc.InfoValueStringType))
			strBldr.Append(driverVersion())
		case adbc.InfoDriverArrowVersion:
			valueBldr.Append(int8(adbc.InfoValueStringType))
			strBldr.Append("arrow-go/v18")
		case adbc.InfoDriverADBCVersion:
			valueBldr.Append(int8(adbc.InfoValueInt64Type))
			int64Bldr.Append(adbc.AdbcVersion1_1_0)
		default:
			valueBldr.Append(int8(adbc.InfoValueStringType))
			strBldr.AppendNull()
		}
	}
	rec := bldr.NewRecord()
	defer rec.Release()
	return array.NewRecordReader(adbc.GetInfoSchema, []arrow.Record{rec})
}

// ---- GetTableTypes ----

func (c *connectionImpl) getTableTypesImpl(context.Context) (array.RecordReader, error) {
	bldr := array.NewRecordBuilder(c.alloc, adbc.TableTypesSchema)
	defer bldr.Release()
	tb := bldr.Field(0).(*array.StringBuilder)
	for _, t := range []string{"TABLE", "VIEW", "ALIAS", "SYSTEM TABLE", "NICKNAME", "MATERIALIZED QUERY TABLE", "GLOBAL TEMPORARY"} {
		tb.Append(t)
	}
	rec := bldr.NewRecord()
	defer rec.Release()
	return array.NewRecordReader(adbc.TableTypesSchema, []arrow.Record{rec})
}

// db2TableType maps SYSCAT.TABLES.TYPE to the ADBC/JDBC table type names.
func db2TableType(t string) string {
	switch strings.TrimSpace(t) {
	case "T":
		return "TABLE"
	case "V":
		return "VIEW"
	case "A":
		return "ALIAS"
	case "S":
		return "MATERIALIZED QUERY TABLE"
	case "N":
		return "NICKNAME"
	case "G":
		return "GLOBAL TEMPORARY"
	case "H":
		return "HIERARCHY TABLE"
	case "L":
		return "DETACHED TABLE"
	case "U":
		return "TYPED TABLE"
	case "W":
		return "TYPED VIEW"
	}
	return "TABLE"
}

// tableTypeFilter renders a SYSCAT.TABLES predicate for ADBC table types.
func tableTypeFilter(types []string) string {
	if len(types) == 0 {
		return ""
	}
	var codes []string
	for _, t := range types {
		switch strings.ToUpper(strings.TrimSpace(t)) {
		case "TABLE", "BASE TABLE":
			codes = append(codes, "'T'")
		case "VIEW":
			codes = append(codes, "'V'")
		case "ALIAS", "SYNONYM":
			codes = append(codes, "'A'")
		case "MATERIALIZED QUERY TABLE", "SUMMARY TABLE":
			codes = append(codes, "'S'")
		case "NICKNAME":
			codes = append(codes, "'N'")
		case "GLOBAL TEMPORARY":
			codes = append(codes, "'G'")
		case "SYSTEM TABLE":
			codes = append(codes, "'T'")
		}
	}
	if len(codes) == 0 {
		return " AND 1 = 0"
	}
	return " AND T.TYPE IN (" + strings.Join(codes, ", ") + ")"
}

// ---- GetTableSchema ----

func (c *connectionImpl) getTableSchemaImpl(ctx context.Context, catalog, dbSchema *string, tableName string) (*arrow.Schema, error) {
	schema := ""
	if dbSchema != nil && *dbSchema != "" {
		schema = *dbSchema
	} else {
		s, err := c.currentSchema(ctx)
		if err != nil {
			return nil, err
		}
		schema = s
	}
	cols, _, err := c.conn.Describe(ctx, "SELECT * FROM "+quoteIdent(schema)+"."+quoteIdent(tableName))
	if err != nil {
		return nil, fromDRDAError(err)
	}
	if err := c.autoCommitIfNeeded(ctx); err != nil {
		return nil, err
	}
	return schemaFor(cols), nil
}
