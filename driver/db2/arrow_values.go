package db2

import (
	"bytes"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// db2TypeForArrow renders the Db2 column type used when the driver
// creates a table for bulk ingest. varcharLen bounds VARCHAR/VARBINARY
// columns; Arrow fields carrying "db2:type" metadata (as produced by
// this driver's own result schemas) round-trip their original type.
func db2TypeForArrow(f arrow.Field, varcharLen int) (string, error) {
	if v, ok := f.Metadata.GetValue(metaDb2Type); ok && v != "" && !strings.HasPrefix(v, "SQLTYPE_") {
		return v, nil
	}
	switch dt := f.Type.(type) {
	case *arrow.BooleanType:
		return "BOOLEAN", nil
	case *arrow.Int8Type, *arrow.Int16Type, *arrow.Uint8Type:
		return "SMALLINT", nil
	case *arrow.Int32Type, *arrow.Uint16Type:
		return "INTEGER", nil
	case *arrow.Int64Type, *arrow.Uint32Type:
		return "BIGINT", nil
	case *arrow.Uint64Type:
		return "DECIMAL(20,0)", nil
	case *arrow.Float16Type, *arrow.Float32Type:
		return "REAL", nil
	case *arrow.Float64Type:
		return "DOUBLE", nil
	case *arrow.Decimal128Type:
		return db2Decimal(dt.Precision, dt.Scale)
	case *arrow.Decimal256Type:
		return db2Decimal(dt.Precision, dt.Scale)
	case *arrow.StringType, *arrow.LargeStringType, *arrow.StringViewType:
		if varcharLen > maxVarcharLength {
			return fmt.Sprintf("CLOB(%d)", varcharLen), nil
		}
		return fmt.Sprintf("VARCHAR(%d)", varcharLen), nil
	case *arrow.BinaryType, *arrow.LargeBinaryType, *arrow.BinaryViewType:
		if varcharLen > maxVarcharLength {
			return fmt.Sprintf("BLOB(%d)", varcharLen), nil
		}
		return fmt.Sprintf("VARBINARY(%d)", varcharLen), nil
	case *arrow.FixedSizeBinaryType:
		return fmt.Sprintf("BINARY(%d)", dt.ByteWidth), nil
	case *arrow.Date32Type, *arrow.Date64Type:
		return "DATE", nil
	case *arrow.Time32Type, *arrow.Time64Type:
		return "TIME", nil
	case *arrow.TimestampType:
		switch dt.Unit {
		case arrow.Second:
			return "TIMESTAMP(0)", nil
		case arrow.Millisecond:
			return "TIMESTAMP(3)", nil
		case arrow.Microsecond:
			return "TIMESTAMP(6)", nil
		default:
			return "TIMESTAMP(9)", nil
		}
	case *arrow.DictionaryType:
		return db2TypeForArrow(arrow.Field{Name: f.Name, Type: dt.ValueType, Nullable: f.Nullable}, varcharLen)
	}
	return "", fmt.Errorf("Arrow type %s has no Db2 equivalent", f.Type)
}

// columnValues extracts every value of an Arrow array as DRDA values.
func columnValues(col arrow.Array) ([]drda.Value, error) {
	n := col.Len()
	out := make([]drda.Value, n)
	if d, ok := col.(*array.Dictionary); ok {
		dictVals, err := columnValues(d.Dictionary())
		if err != nil {
			return nil, err
		}
		for i := 0; i < n; i++ {
			if d.IsNull(i) {
				continue
			}
			out[i] = dictVals[d.GetValueIndex(i)]
		}
		return out, nil
	}
	set := func(i int, v drda.Value) {
		if !col.IsNull(i) {
			out[i] = v
		}
	}
	switch a := col.(type) {
	case *array.Boolean:
		for i := 0; i < n; i++ {
			set(i, a.Value(i))
		}
	case *array.Int8:
		for i := 0; i < n; i++ {
			set(i, int16(a.Value(i)))
		}
	case *array.Int16:
		for i := 0; i < n; i++ {
			set(i, a.Value(i))
		}
	case *array.Int32:
		for i := 0; i < n; i++ {
			set(i, a.Value(i))
		}
	case *array.Int64:
		for i := 0; i < n; i++ {
			set(i, a.Value(i))
		}
	case *array.Uint8:
		for i := 0; i < n; i++ {
			set(i, int16(a.Value(i)))
		}
	case *array.Uint16:
		for i := 0; i < n; i++ {
			set(i, int32(a.Value(i)))
		}
	case *array.Uint32:
		for i := 0; i < n; i++ {
			set(i, int64(a.Value(i)))
		}
	case *array.Uint64:
		for i := 0; i < n; i++ {
			set(i, drda.Decimal{Unscaled: new(big.Int).SetUint64(a.Value(i)), Scale: 0})
		}
	case *array.Float16:
		for i := 0; i < n; i++ {
			set(i, a.Value(i).Float32())
		}
	case *array.Float32:
		for i := 0; i < n; i++ {
			set(i, a.Value(i))
		}
	case *array.Float64:
		for i := 0; i < n; i++ {
			set(i, a.Value(i))
		}
	case *array.Decimal128:
		scale := a.DataType().(*arrow.Decimal128Type).Scale
		for i := 0; i < n; i++ {
			set(i, db2DecimalValue(a.Value(i).BigInt(), scale))
		}
	case *array.Decimal256:
		scale := a.DataType().(*arrow.Decimal256Type).Scale
		for i := 0; i < n; i++ {
			set(i, db2DecimalValue(a.Value(i).BigInt(), scale))
		}
	// String / binary values are zero-copy views into the record's
	// buffers; the bound record may be released before the rows are
	// sent (boundRows), so copy them out.
	case *array.String:
		for i := 0; i < n; i++ {
			set(i, strings.Clone(a.Value(i)))
		}
	case *array.LargeString:
		for i := 0; i < n; i++ {
			set(i, strings.Clone(a.Value(i)))
		}
	case *array.StringView:
		for i := 0; i < n; i++ {
			set(i, strings.Clone(a.Value(i)))
		}
	case *array.Binary:
		for i := 0; i < n; i++ {
			set(i, bytes.Clone(a.Value(i)))
		}
	case *array.LargeBinary:
		for i := 0; i < n; i++ {
			set(i, bytes.Clone(a.Value(i)))
		}
	case *array.BinaryView:
		for i := 0; i < n; i++ {
			set(i, bytes.Clone(a.Value(i)))
		}
	case *array.FixedSizeBinary:
		for i := 0; i < n; i++ {
			set(i, bytes.Clone(a.Value(i)))
		}
	case *array.Date32:
		for i := 0; i < n; i++ {
			t := a.Value(i).ToTime()
			set(i, drda.Date{Year: t.Year(), Month: int(t.Month()), Day: t.Day()})
		}
	case *array.Date64:
		for i := 0; i < n; i++ {
			t := a.Value(i).ToTime()
			set(i, drda.Date{Year: t.Year(), Month: int(t.Month()), Day: t.Day()})
		}
	case *array.Time32:
		unit := a.DataType().(*arrow.Time32Type).Unit
		for i := 0; i < n; i++ {
			if a.IsNull(i) {
				continue
			}
			t, err := wholeSecondTime(a.Value(i).ToTime(unit))
			if err != nil {
				return nil, err
			}
			set(i, t)
		}
	case *array.Time64:
		unit := a.DataType().(*arrow.Time64Type).Unit
		for i := 0; i < n; i++ {
			if a.IsNull(i) {
				continue
			}
			t, err := wholeSecondTime(a.Value(i).ToTime(unit))
			if err != nil {
				return nil, err
			}
			set(i, t)
		}
	case *array.Timestamp:
		// A time-zone-aware Arrow timestamp is an instant (its values are
		// UTC; the zone only says how to display it). Db2's TIMESTAMP has
		// no zone, so store the instant as UTC, whatever the display zone.
		dt := a.DataType().(*arrow.TimestampType)
		for i := 0; i < n; i++ {
			t := a.Value(i).ToTime(dt.Unit).UTC()
			set(i, drda.Timestamp{
				Date:  drda.Date{Year: t.Year(), Month: int(t.Month()), Day: t.Day()},
				Time:  drda.Time{Hour: t.Hour(), Minute: t.Minute(), Second: t.Second()},
				Nanos: t.Nanosecond(),
			})
		}
	case *array.Null:
	default:
		return nil, fmt.Errorf("cannot bind Arrow type %s", col.DataType())
	}
	return out, nil
}

// recordRows transposes a record into row-major DRDA values.
func recordRows(rec arrow.Record) ([][]drda.Value, error) {
	ncols := int(rec.NumCols())
	cols := make([][]drda.Value, ncols)
	for c := 0; c < ncols; c++ {
		v, err := columnValues(rec.Column(c))
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", rec.Schema().Field(c).Name, err)
		}
		cols[c] = v
	}
	n := int(rec.NumRows())
	rows := make([][]drda.Value, n)
	for r := 0; r < n; r++ {
		row := make([]drda.Value, ncols)
		for c := 0; c < ncols; c++ {
			row[c] = cols[c][r]
		}
		rows[r] = row
	}
	return rows, nil
}

// wholeSecondTime converts a time of day to a Db2 TIME, which has
// whole-second precision. A fractional second is refused rather than
// silently dropped.
func wholeSecondTime(t time.Time) (drda.Time, error) {
	if t.Nanosecond() != 0 {
		return drda.Time{}, fmt.Errorf("time %s has a fractional second, but Db2 TIME has whole-second precision (cast it, or bind a timestamp)",
			t.Format("15:04:05.999999999"))
	}
	return drda.Time{Hour: t.Hour(), Minute: t.Minute(), Second: t.Second()}, nil
}

// db2Decimal renders an Arrow decimal's Db2 type. Db2 has no negative
// scale, so decimal(p, -s) (multiples of 10^s) becomes DECIMAL(p+s, 0),
// which holds every such value exactly (see db2DecimalValue).
func db2Decimal(precision, scale int32) (string, error) {
	if scale < 0 {
		precision, scale = precision-scale, 0
	}
	if precision > 31 {
		return "", fmt.Errorf("decimal precision %d exceeds Db2's maximum of 31", precision)
	}
	return fmt.Sprintf("DECIMAL(%d,%d)", precision, scale), nil
}

// db2DecimalValue converts an Arrow decimal value for Db2. A negative
// scale is folded into the digits (unscaled 99 at scale -2 is 9900), as
// Db2 decimals cannot carry one.
func db2DecimalValue(unscaled *big.Int, scale int32) drda.Decimal {
	if scale < 0 {
		mult := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil)
		return drda.Decimal{Unscaled: unscaled.Mul(unscaled, mult), Scale: 0}
	}
	return drda.Decimal{Unscaled: unscaled, Scale: scale}
}
