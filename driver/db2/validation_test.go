// ADBC conformance suite wiring. Plugs the driver into apache/arrow-adbc's
// generic `validation` framework. Requires a live Db2 (DB2_HOST etc.);
// skipped otherwise.
package db2

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/validation"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/suite"
)

type db2Quirks struct {
	uri           string
	alloc         memory.Allocator
	vendorVersion string
}

func (q *db2Quirks) SetupDriver(t *testing.T) adbc.Driver {
	q.alloc = memory.DefaultAllocator
	return NewDriver(q.alloc)
}

func (q *db2Quirks) TearDownDriver(_ *testing.T, _ adbc.Driver) {}

func (q *db2Quirks) DatabaseOptions() map[string]string {
	return map[string]string{OptionURI: q.uri}
}

func (q *db2Quirks) BindParameter(_ int) string { return "?" }

func (q *db2Quirks) SupportsBulkIngest(mode string) bool {
	switch mode {
	case adbc.OptionValueIngestModeCreate, adbc.OptionValueIngestModeAppend,
		adbc.OptionValueIngestModeReplace, adbc.OptionValueIngestModeCreateAppend:
		return true
	}
	return false
}

func (q *db2Quirks) SupportsConcurrentStatements() bool          { return true }
func (q *db2Quirks) SupportsCurrentCatalogSchema() bool          { return true }
func (q *db2Quirks) SupportsGetSetOptions() bool                 { return true }
func (q *db2Quirks) SupportsExecuteSchema() bool                 { return true }
func (q *db2Quirks) SupportsPartitionedData() bool               { return false }
func (q *db2Quirks) SupportsStatistics() bool                    { return false }
func (q *db2Quirks) SupportsTransactions() bool                  { return true }
func (q *db2Quirks) SupportsGetParameterSchema() bool            { return true }
func (q *db2Quirks) SupportsDynamicParameterBinding() bool       { return true }
func (q *db2Quirks) SupportsErrorIngestIncompatibleSchema() bool { return true }

func (q *db2Quirks) GetMetadata(code adbc.InfoCode) interface{} {
	switch code {
	case adbc.InfoVendorName:
		return VendorName
	case adbc.InfoDriverName:
		return DriverName
	case adbc.InfoDriverArrowVersion:
		return "arrow-go/v18"
	case adbc.InfoVendorSql:
		return true
	case adbc.InfoVendorSubstrait:
		return false
	case adbc.InfoDriverVersion:
		return driverVersion()
	case adbc.InfoDriverADBCVersion:
		return int64(adbc.AdbcVersion1_1_0)
	case adbc.InfoVendorVersion:
		return q.vendorVersion
	}
	return nil
}

func (q *db2Quirks) CreateSampleTable(tableName string, r arrow.RecordBatch) error {
	driver := NewDriver(q.alloc)
	db, err := driver.NewDatabase(q.DatabaseOptions())
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Open(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = q.DropTable(conn, tableName)
	stmt, err := conn.NewStatement()
	if err != nil {
		return err
	}
	defer stmt.Close()
	if err := stmt.SetOption(adbc.OptionKeyIngestTargetTable, tableName); err != nil {
		return err
	}
	if err := stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeCreate); err != nil {
		return err
	}
	if err := stmt.Bind(context.Background(), r); err != nil {
		return err
	}
	_, err = stmt.ExecuteUpdate(context.Background())
	return err
}

// SampleTableSchemaMetadata mirrors the db2:* metadata the driver
// attaches to result fields, for the sample table created by bulk
// ingest (int64 → BIGINT, utf8 → auto-sized VARCHAR(64)).
func (q *db2Quirks) SampleTableSchemaMetadata(_ string, dt arrow.DataType) arrow.Metadata {
	switch dt.ID() {
	case arrow.INT64:
		return arrow.NewMetadata([]string{metaDb2Type, metaDb2Length, metaDb2Precision, metaDb2Scale}, []string{"BIGINT", "8", "0", "0"})
	case arrow.STRING:
		return arrow.NewMetadata([]string{metaDb2Type, metaDb2Length, metaDb2Precision, metaDb2Scale}, []string{"VARCHAR(64)", "64", "0", "0"})
	}
	return arrow.Metadata{}
}

func (q *db2Quirks) DropTable(conn adbc.Connection, tableName string) error {
	stmt, err := conn.NewStatement()
	if err != nil {
		return err
	}
	defer stmt.Close()
	if err := stmt.SetSqlQuery("DROP TABLE " + quoteIdent(tableName)); err != nil {
		return err
	}
	_, err = stmt.ExecuteUpdate(context.Background())
	var ae adbc.Error
	if errors.As(err, &ae) && ae.Code == adbc.StatusNotFound {
		return nil
	}
	return err
}

func (q *db2Quirks) Catalog() string {
	if c := os.Getenv("DB2_DATABASE"); c != "" {
		return c
	}
	return "TESTDB"
}

func (q *db2Quirks) DBSchema() string {
	if s := os.Getenv("DB2_SCHEMA"); s != "" {
		return s
	}
	return "DB2INST1"
}

func (q *db2Quirks) Alloc() memory.Allocator { return q.alloc }

// TestValidation runs the apache/arrow-adbc conformance suite against a
// live Db2. Skipped unless DB2_HOST is set.
func TestValidation(t *testing.T) {
	uri := testURI(t)
	q := &db2Quirks{uri: uri}
	// Learn the server's product id once so GetInfo can be checked.
	conn := openConn(t)
	q.vendorVersion = conn.conn.Server.ProductID
	conn.Close()
	suite.Run(t, &validation.DatabaseTests{Quirks: q})
	suite.Run(t, &validation.ConnectionTests{Quirks: q})
	suite.Run(t, &validation.StatementTests{Quirks: q})
}
