package db2

import (
	"context"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

func sampleRecord(t *testing.T, n int) arrow.Record {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int32, Nullable: false},
		{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "amount", Type: &arrow.Decimal128Type{Precision: 12, Scale: 2}, Nullable: true},
		{Name: "ratio", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
		{Name: "flag", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
		{Name: "when", Type: &arrow.TimestampType{Unit: arrow.Microsecond}, Nullable: true},
		{Name: "day", Type: arrow.FixedWidthTypes.Date32, Nullable: true},
		{Name: "blob", Type: arrow.BinaryTypes.Binary, Nullable: true},
	}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	for i := 0; i < n; i++ {
		b.Field(0).(*array.Int32Builder).Append(int32(i))
		if i%7 == 3 {
			b.Field(1).AppendNull()
		} else {
			b.Field(1).(*array.StringBuilder).Append("name-" + itoa(int64(i)) + " ü")
		}
		b.Field(2).(*array.Decimal128Builder).Append(decimal128.FromI64(int64(i * 125)))
		b.Field(3).(*array.Float64Builder).Append(float64(i) / 4)
		b.Field(4).(*array.BooleanBuilder).Append(i%2 == 0)
		b.Field(5).(*array.TimestampBuilder).Append(arrow.Timestamp(1_700_000_000_000_000 + int64(i)*1_000_000))
		b.Field(6).(*array.Date32Builder).Append(arrow.Date32(19_000 + i))
		b.Field(7).(*array.BinaryBuilder).Append([]byte{byte(i), 0xFF, byte(i >> 8)})
	}
	return b.NewRecord()
}

func TestADBCBulkIngest(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	defer stmt.Close()
	_ = stmt.SetSqlQuery("DROP TABLE ADBC_INGEST")
	_, _ = stmt.ExecuteUpdate(ctx)
	t.Cleanup(func() {
		_ = stmt.SetSqlQuery("DROP TABLE ADBC_INGEST")
		_, _ = stmt.ExecuteUpdate(ctx)
	})

	rec := sampleRecord(t, 5000)
	defer rec.Release()

	// create
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestTargetTable, "ADBC_INGEST"))
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeCreate))
	require.NoError(t, stmt.Bind(ctx, rec))
	n, err := stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5000, n)

	// create again → error (spec: "error if the table exists")
	require.NoError(t, stmt.Bind(ctx, rec))
	_, err = stmt.ExecuteUpdate(ctx)
	var ae adbc.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, adbc.StatusInternal, ae.Code)

	// append
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeAppend))
	require.NoError(t, stmt.Bind(ctx, rec))
	n, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5000, n)

	// read back
	q, _ := conn.NewStatement()
	defer q.Close()
	require.NoError(t, q.SetSqlQuery(`SELECT COUNT(*) AS C, SUM("id") AS S, COUNT("name") AS N, MAX("amount") AS A, MAX("when") AS W FROM ADBC_INGEST`))
	rr, _, err := q.ExecuteQuery(ctx)
	require.NoError(t, err)
	recs := readAll(t, rr)
	require.Len(t, recs, 1)
	require.EqualValues(t, 10000, recs[0].Column(0).(*array.Int32).Value(0))
	require.EqualValues(t, 2*4999*5000/2, recs[0].Column(1).(*array.Int32).Value(0))
	require.Equal(t, "6248.75", recs[0].Column(3).(*array.Decimal128).ValueStr(0))
	t.Logf("readback: %v", recs[0])

	// replace with a stream of two batches
	rec2 := sampleRecord(t, 10)
	defer rec2.Release()
	stream, err := array.NewRecordReader(rec2.Schema(), []arrow.Record{rec2, rec2})
	require.NoError(t, err)
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeReplace))
	require.NoError(t, stmt.BindStream(ctx, stream))
	n, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 20, n)

	// create_append on existing
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeCreateAppend))
	require.NoError(t, stmt.Bind(ctx, rec2))
	n, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 10, n)

	// schema round-trip: the created table's schema should match
	schema, err := conn.GetTableSchema(ctx, nil, nil, "ADBC_INGEST")
	require.NoError(t, err)
	t.Logf("created schema: %v", schema)
	require.Equal(t, arrow.PrimitiveTypes.Int32, schema.Field(0).Type)
	require.False(t, schema.Field(0).Nullable)
	require.Equal(t, &arrow.Decimal128Type{Precision: 12, Scale: 2}, schema.Field(2).Type)
	require.Equal(t, arrow.FixedWidthTypes.Boolean, schema.Field(4).Type)
	require.Equal(t, arrow.Microsecond, schema.Field(5).Type.(*arrow.TimestampType).Unit)
}

func TestADBCBindParameters(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	stmt, _ := conn.NewStatement()
	defer stmt.Close()
	_ = stmt.SetSqlQuery("DROP TABLE ADBC_BIND")
	_, _ = stmt.ExecuteUpdate(ctx)
	require.NoError(t, stmt.SetSqlQuery("CREATE TABLE ADBC_BIND (ID INTEGER NOT NULL, V VARCHAR(20))"))
	_, err := stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = stmt.SetSqlQuery("DROP TABLE ADBC_BIND")
		_, _ = stmt.ExecuteUpdate(ctx)
	})

	require.NoError(t, stmt.SetSqlQuery("INSERT INTO ADBC_BIND VALUES (?, ?)"))
	ps, err := stmt.GetParameterSchema()
	require.NoError(t, err)
	require.Equal(t, 2, ps.NumFields())
	require.Equal(t, arrow.PrimitiveTypes.Int32, ps.Field(0).Type)
	require.Equal(t, arrow.BinaryTypes.String, ps.Field(1).Type)

	schema := arrow.NewSchema([]arrow.Field{
		{Name: "0", Type: arrow.PrimitiveTypes.Int32},
		{Name: "1", Type: arrow.BinaryTypes.String, Nullable: true},
	}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	b.Field(0).(*array.Int32Builder).AppendValues([]int32{1, 2, 3}, nil)
	b.Field(1).(*array.StringBuilder).AppendValues([]string{"one", "", "three"}, []bool{true, false, true})
	rec := b.NewRecord()
	defer rec.Release()
	require.NoError(t, stmt.Bind(ctx, rec))
	n, err := stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)

	require.NoError(t, stmt.SetSqlQuery("SELECT ID, V FROM ADBC_BIND WHERE ID = ? ORDER BY ID"))
	b2 := array.NewRecordBuilder(memory.DefaultAllocator, arrow.NewSchema([]arrow.Field{{Name: "0", Type: arrow.PrimitiveTypes.Int64}}, nil))
	defer b2.Release()
	b2.Field(0).(*array.Int64Builder).Append(3)
	one := b2.NewRecord()
	defer one.Release()
	require.NoError(t, stmt.Bind(ctx, one))
	rr, _, err := stmt.ExecuteQuery(ctx)
	require.NoError(t, err)
	recs := readAll(t, rr)
	require.Len(t, recs, 1)
	require.EqualValues(t, 1, recs[0].NumRows())
	require.Equal(t, "three", recs[0].Column(1).(*array.String).Value(0))

	// ExecuteSchema
	require.NoError(t, stmt.SetSqlQuery("SELECT ID, V FROM ADBC_BIND"))
	es, err := stmt.(adbc.StatementExecuteSchema).ExecuteSchema(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, es.NumFields())
}
