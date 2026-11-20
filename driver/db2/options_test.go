package db2

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/stretchr/testify/require"
)

// Hermetic tests of the option names (ADBC Driver Foundry style guide) and
// the deprecated pre-0.3.0 `adbc.db2.*` aliases. No server needed.

// sampleValues gives each database option a valid value (parseOptions
// accepts it) that differs from the default.
var sampleValues = map[string]string{
	OptionHost:              "db2.example.invalid",
	OptionPort:              "50123",
	OptionDatabase:          "SAMPLE",
	OptionTLS:               "false",
	OptionTLSCACert:         "/nonexistent/ca.pem",
	OptionTLSSkipVerify:     "true",
	OptionSecurityMechanism: "3",
	OptionCurrentSchema:     "SALES",
	OptionApplicationName:   "nightly-load",
	OptionConnectTimeout:    "7",
	OptionQueryBlockSize:    "65536",
	OptionBatchSize:         "1234",
	OptionBatchBytes:        "4096",
	OptionTrace:             "false",
	OptionTraceFile:         "",
	OptionPackage:           "MYCOLL.MYPKG",
	OptionNoAutoBind:        "true",
}

// captureWarnings sends deprecation warnings to a buffer for the test.
func captureWarnings(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	t.Cleanup(setDeprecationOutput(buf))
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestDatabase(t *testing.T, opts map[string]string) *databaseImpl {
	t.Helper()
	db, err := NewDriver(nil).NewDatabase(opts)
	require.NoError(t, err)
	return db.(*databaseImpl)
}

func TestOptionNamesFollowStyleGuide(t *testing.T) {
	spec := map[string]bool{OptionURI: true, OptionUsername: true, OptionPassword: true}
	for old, newKey := range deprecatedOptions {
		require.True(t, strings.HasPrefix(old, "adbc.db2."), old)
		require.True(t, strings.HasPrefix(newKey, "db2."), "%s must start with the vendor prefix", newKey)
		require.False(t, strings.HasPrefix(newKey, "adbc."), newKey)
	}
	for key := range databaseOptions {
		if spec[key] {
			continue
		}
		require.True(t, strings.HasPrefix(key, "db2."), key)
	}
	require.Len(t, deprecatedOptions, 20, "every pre-0.3.0 key keeps an alias")
}

func TestDeprecatedDatabaseOptionSetsTheSameThing(t *testing.T) {
	captureWarnings(t)
	base := map[string]string{OptionURI: "db2://user@db2.example.invalid:50000/SAMPLE"}
	for old, newKey := range deprecatedOptions {
		value, ok := sampleValues[newKey]
		if !ok {
			continue // statement or read-only connection option
		}
		t.Run(old, func(t *testing.T) {
			viaNew := cloneMap(base)
			viaNew[newKey] = value
			viaOld := cloneMap(base)
			viaOld[old] = value

			dbNew := newTestDatabase(t, viaNew)
			dbOld := newTestDatabase(t, viaOld)
			require.Equal(t, dbNew.opts, dbOld.opts, "the old key is stored under the new name")

			// GetOption answers through both names.
			for _, db := range []*databaseImpl{dbNew, dbOld} {
				got, err := db.GetOption(newKey)
				require.NoError(t, err)
				require.Equal(t, value, got)
				got, err = db.GetOption(old)
				require.NoError(t, err)
				require.Equal(t, value, got)
			}

			cfgNew, errNew := parseOptions(dbNew.opts)
			cfgOld, errOld := parseOptions(dbOld.opts)
			if newKey == OptionTLSCACert {
				// TLS is off here, so the CA path is never read.
				require.NoError(t, errNew)
			}
			require.Equal(t, errNew, errOld)
			require.Equal(t, cfgNew, cfgOld)
		})
	}
}

func TestDeprecatedOptionSetOptionAndSetOptions(t *testing.T) {
	captureWarnings(t)
	db := newTestDatabase(t, nil)
	require.NoError(t, db.SetOption("adbc.db2.batch_size", "77"))
	require.NoError(t, db.SetOptions(map[string]string{"adbc.db2.batch_bytes": "99"}))
	require.NoError(t, db.SetOptionInt("adbc.db2.query_block_size", 32768))

	for key, want := range map[string]string{
		OptionBatchSize: "77", "adbc.db2.batch_size": "77",
		OptionBatchBytes: "99", "adbc.db2.batch_bytes": "99",
		OptionQueryBlockSize: "32768", "adbc.db2.query_block_size": "32768",
	} {
		got, err := db.GetOption(key)
		require.NoError(t, err, key)
		require.Equal(t, want, got, key)
	}
	n, err := db.GetOptionInt("adbc.db2.batch_size")
	require.NoError(t, err)
	require.Equal(t, int64(77), n)
	_, present := db.opts["adbc.db2.batch_size"]
	require.False(t, present, "nothing is stored under the old name")
}

func TestNewNameTakesPrecedence(t *testing.T) {
	captureWarnings(t)

	// Both in one map, whatever the map's iteration order.
	for i := 0; i < 50; i++ {
		db := newTestDatabase(t, map[string]string{
			"adbc.db2.batch_size": "1",
			OptionBatchSize:       "2",
		})
		got, err := db.GetOption(OptionBatchSize)
		require.NoError(t, err)
		require.Equal(t, "2", got)
	}

	// New name first, old name later: the old key does not override.
	db := newTestDatabase(t, nil)
	require.NoError(t, db.SetOption(OptionBatchSize, "2"))
	require.NoError(t, db.SetOption("adbc.db2.batch_size", "1"))
	got, err := db.GetOption("adbc.db2.batch_size")
	require.NoError(t, err)
	require.Equal(t, "2", got)

	// Old name first, new name later: the new one replaces it.
	db = newTestDatabase(t, nil)
	require.NoError(t, db.SetOption("adbc.db2.batch_size", "1"))
	require.NoError(t, db.SetOption(OptionBatchSize, "2"))
	got, err = db.GetOption(OptionBatchSize)
	require.NoError(t, err)
	require.Equal(t, "2", got)

	// Both in one SetOptions call.
	db = newTestDatabase(t, nil)
	require.NoError(t, db.SetOptions(map[string]string{OptionBatchSize: "2", "adbc.db2.batch_size": "1"}))
	got, err = db.GetOption(OptionBatchSize)
	require.NoError(t, err)
	require.Equal(t, "2", got)

	// Statement options too.
	st := &statementImpl{}
	require.NoError(t, st.SetOption(OptionIngestBatchRows, "20"))
	require.NoError(t, st.SetOption("adbc.db2.ingest.batch_rows", "10"))
	require.Equal(t, 20, st.ingestBatchRows)
	st = &statementImpl{}
	require.NoError(t, st.SetOption("adbc.db2.ingest.batch_rows", "10"))
	require.NoError(t, st.SetOption(OptionIngestBatchRows, "20"))
	require.Equal(t, 20, st.ingestBatchRows)
}

func TestDeprecatedStatementOptions(t *testing.T) {
	captureWarnings(t)
	viaNew, viaOld := &statementImpl{}, &statementImpl{}
	require.NoError(t, viaNew.SetOption(OptionIngestVarcharLength, "300"))
	require.NoError(t, viaOld.SetOption("adbc.db2.ingest.varchar_length", "300"))
	require.NoError(t, viaNew.SetOptionInt(OptionIngestBatchRows, 50))
	require.NoError(t, viaOld.SetOptionInt("adbc.db2.ingest.batch_rows", 50))
	require.Equal(t, viaNew.ingestVarcharLength, viaOld.ingestVarcharLength)
	require.Equal(t, viaNew.ingestBatchRows, viaOld.ingestBatchRows)

	for _, key := range []string{OptionIngestVarcharLength, "adbc.db2.ingest.varchar_length"} {
		got, err := viaOld.GetOption(key)
		require.NoError(t, err)
		require.Equal(t, "300", got)
	}
	for _, key := range []string{OptionIngestBatchRows, "adbc.db2.ingest.batch_rows"} {
		n, err := viaOld.GetOptionInt(key)
		require.NoError(t, err)
		require.Equal(t, int64(50), n)
	}

	// Validation applies to the old name too.
	err := viaOld.SetOption("adbc.db2.ingest.batch_rows", "-1")
	var ae adbc.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, adbc.StatusInvalidArgument, ae.Code)
	require.Equal(t, `[db2] invalid value "-1" for db2.ingest.batch_rows`, ae.Msg)
}

func TestDeprecatedConnectionGetOption(t *testing.T) {
	captureWarnings(t)
	c := &connectionImpl{cfg: &connConfig{batchSize: 321, batchBytes: 654}}
	for _, key := range []string{OptionBatchSize, "adbc.db2.batch_size"} {
		got, err := c.GetOption(key)
		require.NoError(t, err)
		require.Equal(t, "321", got)
	}
	for _, key := range []string{OptionBatchBytes, "adbc.db2.batch_bytes"} {
		got, err := c.GetOption(key)
		require.NoError(t, err)
		require.Equal(t, "654", got)
	}
}

func TestDeprecationWarningPrintedOncePerKey(t *testing.T) {
	buf := captureWarnings(t)
	db := newTestDatabase(t, map[string]string{"adbc.db2.batch_size": "5"})
	require.NoError(t, db.SetOption("adbc.db2.batch_size", "6"))
	_, err := db.GetOption("adbc.db2.batch_size")
	require.NoError(t, err)
	newTestDatabase(t, map[string]string{"adbc.db2.batch_size": "7"})
	// A different old key gets its own line; new names print nothing.
	require.NoError(t, db.SetOption("adbc.db2.trace", "false"))
	require.NoError(t, db.SetOption(OptionBatchBytes, "1"))

	require.Equal(t,
		"[db2] option \"adbc.db2.batch_size\" is deprecated; use \"db2.query.batch_rows\"\n"+
			"[db2] option \"adbc.db2.trace\" is deprecated; use \"db2.trace\"\n",
		buf.String())
}

func TestDeprecationWarningOnceUnderConcurrency(t *testing.T) {
	buf := captureWarnings(t)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, deprecated := canonicalOption("adbc.db2.port")
			if !deprecated || got != OptionPort {
				panic(fmt.Sprintf("canonicalOption: %q %v", got, deprecated))
			}
		}()
	}
	wg.Wait()
	require.Equal(t, "[db2] option \"adbc.db2.port\" is deprecated; use \"db2.port\"\n", buf.String())
}

func TestDeprecationWarningNeverShowsTheValue(t *testing.T) {
	buf := captureWarnings(t)
	const secret = "hunter2-S3cr3t-t0ken"
	db := newTestDatabase(t, nil)
	st := &statementImpl{}
	for old := range deprecatedOptions {
		// Some setters reject the value; the warning must not carry it
		// either way.
		_ = db.SetOption(old, secret)
		_ = st.SetOption(old, secret)
	}
	out := buf.String()
	require.Equal(t, len(deprecatedOptions), strings.Count(out, "\n"), out)
	require.NotContains(t, out, secret)
	require.NotContains(t, out, "hunter2")
}

func TestErrorMessagesUseDriverPrefix(t *testing.T) {
	db := newTestDatabase(t, nil)
	err := db.SetOption("db2.no_such_option", "x")
	var ae adbc.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, adbc.StatusNotImplemented, ae.Code)
	require.Equal(t, `[db2] unknown database option "db2.no_such_option"`, ae.Msg)

	_, err = parseOptions(map[string]string{OptionURI: "mysql://h/db"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, `[db2] unsupported URI scheme "mysql" (want db2://)`, ae.Msg)

	_, err = parseOptions(map[string]string{OptionURI: "db2://u@h/SAMPLE", OptionPort: "x"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, `[db2] invalid db2.port "x"`, ae.Msg)

	err = fromDRDAError(errors.New("connection reset"))
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "[db2] connection reset", ae.Msg)

	// Never prefixed twice.
	err = fromDRDAError(errStatus(adbc.StatusIO, "boom"))
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "[db2] boom", ae.Msg)
	err = fromDRDAError(adbc.Error{Code: adbc.StatusIO, Msg: "plain"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "[db2] plain", ae.Msg)
}

func TestURIParametersMatchOptionNames(t *testing.T) {
	for _, tc := range []struct{ params, alias string }{
		{"batch_size=123", "batch_rows=123"},
		{"query_block_size=65536", "block_size=65536"},
		{"connect_timeout=7", "connect_timeout_seconds=7"},
	} {
		viaOld, err := parseOptions(map[string]string{OptionURI: "db2://u@h/SAMPLE?" + tc.params})
		require.NoError(t, err)
		viaNew, err := parseOptions(map[string]string{OptionURI: "db2://u@h/SAMPLE?" + tc.alias})
		require.NoError(t, err)
		require.Equal(t, viaOld, viaNew, tc.alias)
	}
	cfg, err := parseOptions(map[string]string{OptionURI: "db2://u@h/SAMPLE?batch_rows=123&connect_timeout_seconds=7"})
	require.NoError(t, err)
	require.Equal(t, 123, cfg.batchSize)
	require.Equal(t, int64(7e9), int64(cfg.drda.ConnectTimeout))

	// An option beats the URI parameter for the same setting.
	cfg, err = parseOptions(map[string]string{OptionURI: "db2://u@h/SAMPLE?batch_rows=123", OptionBatchSize: "456"})
	require.NoError(t, err)
	require.Equal(t, 456, cfg.batchSize)
}
