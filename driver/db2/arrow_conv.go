package db2

import (
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// Arrow field metadata keys carrying Db2 type details.
const (
	metaDb2Type      = "db2:type"
	metaDb2Length    = "db2:length"
	metaDb2Precision = "db2:precision"
	metaDb2Scale     = "db2:scale"
)

// arrowFieldFor maps a Db2 column description to an Arrow field.
func arrowFieldFor(col drda.ColumnDesc) arrow.Field {
	var dt arrow.DataType
	typeName := ""
	switch col.Base() {
	case drda.SQLTypeSmallint:
		dt, typeName = arrow.PrimitiveTypes.Int16, "SMALLINT"
	case drda.SQLTypeInteger:
		dt, typeName = arrow.PrimitiveTypes.Int32, "INTEGER"
	case drda.SQLTypeBigint:
		dt, typeName = arrow.PrimitiveTypes.Int64, "BIGINT"
	case drda.SQLTypeFloat:
		if col.Length == 4 {
			dt, typeName = arrow.PrimitiveTypes.Float32, "REAL"
		} else {
			dt, typeName = arrow.PrimitiveTypes.Float64, "DOUBLE"
		}
	case drda.SQLTypeDecimal, drda.SQLTypeNumeric, drda.SQLTypeZoned:
		p, s := col.Precision, col.Scale
		if p <= 0 {
			p = 31
		}
		dt = &arrow.Decimal128Type{Precision: p, Scale: s}
		typeName = fmt.Sprintf("DECIMAL(%d,%d)", p, s)
	case drda.SQLTypeDecFloat:
		// DECFLOAT has a per-value exponent; Arrow decimals have a fixed
		// scale, so the exact textual form is the lossless choice.
		dt = arrow.BinaryTypes.String
		if col.Length == 16 {
			typeName = "DECFLOAT(34)"
		} else {
			typeName = "DECFLOAT(16)"
		}
	case drda.SQLTypeBoolean:
		dt, typeName = arrow.FixedWidthTypes.Boolean, "BOOLEAN"
	case drda.SQLTypeDate:
		dt, typeName = arrow.FixedWidthTypes.Date32, "DATE"
	case drda.SQLTypeTime:
		dt, typeName = arrow.FixedWidthTypes.Time32s, "TIME"
	case drda.SQLTypeTimestamp:
		// Length 19 = TIMESTAMP(0), 26 = TIMESTAMP(6), 20+p otherwise.
		frac := int(col.Length) - 20
		switch {
		case frac <= 0:
			dt = &arrow.TimestampType{Unit: arrow.Second}
		case frac <= 3:
			dt = &arrow.TimestampType{Unit: arrow.Millisecond}
		case frac <= 6:
			dt = &arrow.TimestampType{Unit: arrow.Microsecond}
		default:
			dt = &arrow.TimestampType{Unit: arrow.Nanosecond}
		}
		if frac < 0 {
			frac = 0
		}
		typeName = fmt.Sprintf("TIMESTAMP(%d)", frac)
	case drda.SQLTypeChar:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("CHAR(%d)", col.Length)
	case drda.SQLTypeVarchar:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("VARCHAR(%d)", col.Length)
	case drda.SQLTypeLongVarchar:
		dt, typeName = arrow.BinaryTypes.String, "LONG VARCHAR"
	case drda.SQLTypeGraphic:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("GRAPHIC(%d)", col.Length)
	case drda.SQLTypeVargraphic:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("VARGRAPHIC(%d)", col.Length)
	case drda.SQLTypeLongVargraph:
		dt, typeName = arrow.BinaryTypes.String, "LONG VARGRAPHIC"
	case drda.SQLTypeClob, drda.SQLTypeClobLocator:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("CLOB(%d)", col.Length)
	case drda.SQLTypeDBClob, drda.SQLTypeDBClobLocator:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("DBCLOB(%d)", col.Length)
	case drda.SQLTypeXML:
		dt, typeName = arrow.BinaryTypes.String, "XML"
	case drda.SQLTypeBinary:
		dt, typeName = arrow.BinaryTypes.Binary, fmt.Sprintf("BINARY(%d)", col.Length)
	case drda.SQLTypeVarbinary:
		dt, typeName = arrow.BinaryTypes.Binary, fmt.Sprintf("VARBINARY(%d)", col.Length)
	case drda.SQLTypeBlob, drda.SQLTypeBlobLocator:
		dt, typeName = arrow.BinaryTypes.Binary, fmt.Sprintf("BLOB(%d)", col.Length)
	case drda.SQLTypeRowID:
		dt, typeName = arrow.BinaryTypes.Binary, "ROWID"
	default:
		dt, typeName = arrow.BinaryTypes.String, fmt.Sprintf("SQLTYPE_%d", col.Base())
	}
	md := arrow.NewMetadata(
		[]string{metaDb2Type, metaDb2Length, metaDb2Precision, metaDb2Scale},
		[]string{typeName, strconv.FormatInt(col.Length, 10), strconv.Itoa(int(col.Precision)), strconv.Itoa(int(col.Scale))},
	)
	return arrow.Field{Name: col.Name, Type: dt, Nullable: col.Nullable(), Metadata: md}
}

// schemaFor builds the Arrow schema for a result set.
func schemaFor(cols []drda.ColumnDesc) *arrow.Schema {
	fields := make([]arrow.Field, len(cols))
	for i, c := range cols {
		fields[i] = arrowFieldFor(c)
	}
	return arrow.NewSchema(fields, nil)
}

// recordFromRows converts decoded DRDA rows into one Arrow record.
func recordFromRows(alloc memory.Allocator, schema *arrow.Schema, rows [][]drda.Value) (arrow.Record, error) {
	if alloc == nil {
		alloc = memory.NewGoAllocator()
	}
	bldr := array.NewRecordBuilder(alloc, schema)
	defer bldr.Release()
	bldr.Reserve(len(rows))
	for ci, f := range schema.Fields() {
		fb := bldr.Field(ci)
		for _, row := range rows {
			if err := appendValue(fb, f.Type, row[ci]); err != nil {
				return nil, fmt.Errorf("column %q: %w", f.Name, err)
			}
		}
	}
	return bldr.NewRecord(), nil
}

func appendValue(b array.Builder, dt arrow.DataType, v drda.Value) error {
	if v == nil {
		b.AppendNull()
		return nil
	}
	switch bb := b.(type) {
	case *array.Int16Builder:
		n, ok := toInt64(v)
		if !ok {
			return typeErr(v, dt)
		}
		bb.Append(int16(n))
	case *array.Int32Builder:
		n, ok := toInt64(v)
		if !ok {
			return typeErr(v, dt)
		}
		bb.Append(int32(n))
	case *array.Int64Builder:
		n, ok := toInt64(v)
		if !ok {
			return typeErr(v, dt)
		}
		bb.Append(n)
	case *array.Float32Builder:
		switch x := v.(type) {
		case float32:
			bb.Append(x)
		case float64:
			bb.Append(float32(x))
		default:
			return typeErr(v, dt)
		}
	case *array.Float64Builder:
		switch x := v.(type) {
		case float64:
			bb.Append(x)
		case float32:
			bb.Append(float64(x))
		default:
			return typeErr(v, dt)
		}
	case *array.BooleanBuilder:
		x, ok := v.(bool)
		if !ok {
			return typeErr(v, dt)
		}
		bb.Append(x)
	case *array.Decimal128Builder:
		d, ok := v.(drda.Decimal)
		if !ok {
			return typeErr(v, dt)
		}
		scale := dt.(*arrow.Decimal128Type).Scale
		n, err := rescale(d, scale)
		if err != nil {
			return err
		}
		bb.Append(n)
	case *array.StringBuilder:
		switch x := v.(type) {
		case string:
			bb.Append(x)
		case drda.Decimal:
			bb.Append(x.String())
		case float64:
			if math.IsNaN(x) {
				bb.Append("NaN")
			} else if math.IsInf(x, 1) {
				bb.Append("Infinity")
			} else if math.IsInf(x, -1) {
				bb.Append("-Infinity")
			} else {
				bb.Append(strconv.FormatFloat(x, 'g', -1, 64))
			}
		case []byte:
			bb.Append(string(x))
		case drda.Date, drda.Time:
			bb.Append(fmt.Sprint(x))
		case drda.Timestamp:
			bb.Append(x.ToTime().Format("2006-01-02 15:04:05.999999999"))
		default:
			bb.Append(fmt.Sprint(x))
		}
	case *array.BinaryBuilder:
		switch x := v.(type) {
		case []byte:
			bb.Append(x)
		case string:
			bb.Append([]byte(x))
		default:
			return typeErr(v, dt)
		}
	case *array.Date32Builder:
		d, ok := v.(drda.Date)
		if !ok {
			return typeErr(v, dt)
		}
		bb.Append(arrow.Date32(d.DaysSinceEpoch()))
	case *array.Time32Builder:
		t, ok := v.(drda.Time)
		if !ok {
			return typeErr(v, dt)
		}
		bb.Append(arrow.Time32(t.Hour*3600 + t.Minute*60 + t.Second))
	case *array.TimestampBuilder:
		ts, ok := v.(drda.Timestamp)
		if !ok {
			return typeErr(v, dt)
		}
		t := ts.ToTime()
		switch dt.(*arrow.TimestampType).Unit {
		case arrow.Second:
			bb.Append(arrow.Timestamp(t.Unix()))
		case arrow.Millisecond:
			bb.Append(arrow.Timestamp(t.UnixMilli()))
		case arrow.Microsecond:
			bb.Append(arrow.Timestamp(t.UnixMicro()))
		default:
			bb.Append(arrow.Timestamp(t.UnixNano()))
		}
	default:
		return fmt.Errorf("unsupported Arrow builder %T", b)
	}
	return nil
}

func typeErr(v drda.Value, dt arrow.DataType) error {
	return fmt.Errorf("cannot store %T in Arrow %s", v, dt)
}

func toInt64(v drda.Value) (int64, bool) {
	switch x := v.(type) {
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case int8:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

var bigTen = big.NewInt(10)

func rescale(d drda.Decimal, scale int32) (decimal128.Num, error) {
	n := new(big.Int).Set(d.Unscaled)
	switch {
	case d.Scale < scale:
		n.Mul(n, new(big.Int).Exp(bigTen, big.NewInt(int64(scale-d.Scale)), nil))
	case d.Scale > scale:
		n.Quo(n, new(big.Int).Exp(bigTen, big.NewInt(int64(d.Scale-scale)), nil))
	}
	if n.BitLen() > 127 {
		return decimal128.Num{}, fmt.Errorf("decimal %s overflows decimal128", d)
	}
	return decimal128.FromBigInt(n), nil
}
