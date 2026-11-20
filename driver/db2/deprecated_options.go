package db2

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// deprecatedOptions maps each option key the driver used before 0.3.0 to
// its current name (ADBC Driver Foundry style guide: a `db2.` vendor
// prefix, never `adbc.`). The old keys keep working, for setting and for
// GetOption, and print a one-time warning (see canonicalOption).
var deprecatedOptions = map[string]string{
	"adbc.db2.host":                      OptionHost,
	"adbc.db2.port":                      OptionPort,
	"adbc.db2.database":                  OptionDatabase,
	"adbc.db2.tls":                       OptionTLS,
	"adbc.db2.tls.ca_cert":               OptionTLSCACert,
	"adbc.db2.tls.skip_verify":           OptionTLSSkipVerify,
	"adbc.db2.security_mechanism":        OptionSecurityMechanism,
	"adbc.db2.security_mechanism_active": OptionSecurityMechanismActive,
	"adbc.db2.current_schema":            OptionCurrentSchema,
	"adbc.db2.application_name":          OptionApplicationName,
	"adbc.db2.connect_timeout":           OptionConnectTimeout,
	"adbc.db2.query_block_size":          OptionQueryBlockSize,
	"adbc.db2.batch_size":                OptionBatchSize,
	"adbc.db2.batch_bytes":               OptionBatchBytes,
	"adbc.db2.trace":                     OptionTrace,
	"adbc.db2.trace_file":                OptionTraceFile,
	"adbc.db2.package":                   OptionPackage,
	"adbc.db2.no_auto_bind":              OptionNoAutoBind,
	"adbc.db2.ingest.varchar_length":     OptionIngestVarcharLength,
	"adbc.db2.ingest.batch_rows":         OptionIngestBatchRows,
}

var (
	// deprecationWarned records the old keys already warned about in this
	// process (one warning per old key per process).
	deprecationWarned sync.Map
	// deprecationOut is where the warnings go (stderr; tests swap it).
	deprecationMu  sync.Mutex
	deprecationOut io.Writer = os.Stderr
)

// canonicalOption returns the current name for key: the new name for a
// deprecated key (warning once per process that it is deprecated), else key
// itself. deprecated reports whether key was an old name.
//
// The warning names the two keys only, never the value: options carry
// passwords and tokens.
func canonicalOption(key string) (canonical string, deprecated bool) {
	newKey, ok := deprecatedOptions[key]
	if !ok {
		return key, false
	}
	if _, warned := deprecationWarned.LoadOrStore(key, struct{}{}); !warned {
		deprecationMu.Lock()
		fmt.Fprintf(deprecationOut, "[db2] option %q is deprecated; use %q\n", key, newKey)
		deprecationMu.Unlock()
	}
	return newKey, true
}

// setDeprecationOutput points the deprecation warnings at w and forgets
// which keys were already warned about; it returns a function that
// restores the previous writer. For tests.
func setDeprecationOutput(w io.Writer) (restore func()) {
	deprecationMu.Lock()
	prev := deprecationOut
	deprecationOut = w
	deprecationMu.Unlock()
	deprecationWarned.Clear()
	return func() {
		deprecationMu.Lock()
		deprecationOut = prev
		deprecationMu.Unlock()
		deprecationWarned.Clear()
	}
}
