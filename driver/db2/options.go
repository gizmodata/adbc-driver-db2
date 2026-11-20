// Package db2 implements an Apache Arrow ADBC driver for IBM Db2 over
// the DRDA wire protocol, in pure Go (no IBM CLI / ODBC dependency).
package db2

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// ADBC option keys. Driver-specific keys follow the ADBC Driver Foundry
// style guide (https://adbc-drivers.org/building-drivers/style-guide.html):
// `db2[.group].option`, never `adbc.` (reserved for the ADBC
// specification's own keys), query behaviour under `db2.query.*` and bulk
// ingest under `db2.ingest.*`. The pre-0.3.0 `adbc.db2.*` names still work
// (see deprecatedOptions) and print a one-time deprecation warning.
const (
	// OptionURI is "uri": db2://[user[:password]@]host[:port]/DATABASE[?params]
	OptionURI      = adbc.OptionKeyURI
	OptionUsername = adbc.OptionKeyUsername // "username"
	OptionPassword = adbc.OptionKeyPassword // "password"

	OptionHost     = "db2.host"
	OptionPort     = "db2.port"
	OptionDatabase = "db2.database"
	// OptionTLS enables TLS ("true"/"false"). Db2's SSL port is 50001
	// by convention; IBM Db2 on Cloud requires TLS.
	OptionTLS = "db2.tls"
	// OptionTLSCACert is a path to a PEM CA bundle used to verify the
	// server (for self-signed Db2 certificates).
	OptionTLSCACert = "db2.tls.ca_cert"
	// OptionTLSSkipVerify disables server certificate verification.
	OptionTLSSkipVerify = "db2.tls.skip_verify"
	// OptionSecurityMechanism forces a DRDA SECMEC: "3" (cleartext
	// password), "4" (user id only), "9" (Diffie-Hellman encrypted;
	// default when the server allows it). An explicitly set mechanism
	// fails closed — the connection errors rather than silently
	// downgrading when the server refuses it. Note SECMEC 9 encrypts the
	// credentials only; data in transit is protected by TLS, not SECMEC.
	OptionSecurityMechanism = "db2.auth.security_mechanism"
	// OptionSecurityMechanismActive (read-only, connection) reports the
	// SECMEC that was actually negotiated for the session, e.g. "9" or
	// "3" (it can differ from the preference when chosen automatically).
	OptionSecurityMechanismActive = "db2.auth.security_mechanism_active"
	// OptionCurrentSchema runs SET CURRENT SCHEMA after connecting.
	OptionCurrentSchema = "db2.session.current_schema"
	// OptionApplicationName is reported to the server as the DRDA
	// external name (visible in LIST APPLICATIONS / MON_GET_CONNECTION).
	OptionApplicationName = "db2.session.application_name"
	// OptionConnectTimeout is the connect timeout in seconds (default 30).
	// A Go duration such as "1.5s" is accepted too.
	OptionConnectTimeout = "db2.connect_timeout_seconds"
	// OptionQueryBlockSize is the DRDA QRYBLKSZ in bytes (default 1 MiB;
	// larger blocks mean fewer round trips per result set).
	OptionQueryBlockSize = "db2.query.block_size"
	// OptionBatchSize caps rows per Arrow record batch of a result
	// (default 65536).
	OptionBatchSize = "db2.query.batch_rows"
	// OptionBatchBytes caps the approximate size of an Arrow record batch
	// in bytes (default 8 MiB — keeps a batch under common transport
	// limits such as gRPC's 16 MiB Flight SQL message cap; 0 = unlimited;
	// rows are still capped by db2.query.batch_rows).
	OptionBatchBytes = "db2.query.batch_bytes"
	// OptionTrace: "true" logs DRDA traffic (one line per message plus the
	// SQL text); "hex" additionally dumps reply payloads. Goes to stderr
	// unless OptionTraceFile names a file (appended to) — use that from
	// notebooks, whose cells do not show the process's stderr.
	OptionTrace     = "db2.trace"
	OptionTraceFile = "db2.trace.file"
	// OptionPackage names the dynamic-SQL package as COLLECTION.PKGID
	// (default NULLID.SYSSH200). If the package does not exist on the
	// server (SQL0805N — typical on Db2 for i / z/OS, which do not ship
	// the CLI packages), the driver binds it the way IBM's DB2Binder
	// does; OptionNoAutoBind ("true") disables that.
	OptionPackage    = "db2.package"
	OptionNoAutoBind = "db2.package.no_auto_bind"

	OptionIngestTable = adbc.OptionKeyIngestTargetTable
)

// connConfig is the resolved connection configuration.
type connConfig struct {
	drda       drda.Config
	batchSize  int
	batchBytes int64
	trace      bool
	traceHex   bool
	traceFile  string
}

// parseOptions merges the URI and explicit ADBC options into a config.
// Explicit options override URI components.
func parseOptions(opts map[string]string) (*connConfig, error) {
	// batchBytes defaults to 8 MiB so a single batch stays under common
	// transport limits (e.g. Flight SQL's 16 MiB gRPC message cap) even
	// for wide rows; set batch_bytes=0 for unlimited.
	cfg := &connConfig{batchSize: 65536, batchBytes: 8 << 20}
	cfg.drda.ConnectTimeout = 30 * time.Second
	var tlsEnabled, skipVerify bool
	var caCert string

	// traceSet records an explicit trace setting (URI trace= or db2.trace):
	// it decides, and a trace file only turns tracing on when nothing did.
	var traceSet bool

	if raw := opts[OptionURI]; raw != "" {
		// Never echo the URI or a piece of it in an error: it can carry a
		// password, and a password with an unencoded "/", "@" or "%" lands
		// in the host, the port or a bad escape.
		u, err := url.Parse(raw)
		if err != nil {
			return nil, errStatus(adbc.StatusInvalidArgument, "could not parse the URI: %s", uriProblem(err))
		}
		switch u.Scheme {
		case "db2", "drda", "ibm-db2", "db2s":
		default:
			return nil, errStatus(adbc.StatusInvalidArgument, "unsupported URI scheme %q (want db2://)", u.Scheme)
		}
		if u.Scheme == "db2s" {
			tlsEnabled = true
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			host = u.Host
			port = ""
		}
		cfg.drda.Host = host
		if port != "" {
			p, err := strconv.Atoi(port)
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument,
					"invalid port in the URI (percent-encode any \"/\", \"@\", \":\" or \"%%\" in the user name or password)")
			}
			cfg.drda.Port = p
		}
		if u.User != nil {
			cfg.drda.User = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				cfg.drda.Password = pw
			}
		}
		cfg.drda.Database = strings.Trim(u.Path, "/")
		params, err := uriParameters(u.RawQuery)
		if err != nil {
			return nil, err
		}
		for _, p := range params {
			v := p.value
			switch p.setting {
			case "tls":
				tlsEnabled = isTrue(v) || strings.EqualFold(v, "ssl")
			case "tls_ca_cert":
				caCert = v
			case "tls_skip_verify":
				skipVerify = isTrue(v)
			case "security_mechanism":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, errStatus(adbc.StatusInvalidArgument, "invalid security mechanism %q", v)
				}
				cfg.drda.SecurityMechanism = uint16(n)
			case "current_schema":
				cfg.drda.CurrentSchema = v
			case "block_size":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", p.name, v)
				}
				cfg.drda.QueryBlockSize = uint32(n)
			case "connect_timeout":
				d, err := parseTimeout(v)
				if err != nil {
					return nil, err
				}
				cfg.drda.ConnectTimeout = d
			case "application_name":
				cfg.drda.ApplicationName = v
			case "package":
				coll, pkg, ok := strings.Cut(v, ".")
				if !ok || coll == "" || pkg == "" {
					return nil, errStatus(adbc.StatusInvalidArgument, "package must be COLLECTION.PKGID, got %q", v)
				}
				cfg.drda.PackageCollection, cfg.drda.PackageID = coll, pkg
			case "trace":
				cfg.trace = isTrue(v) || strings.EqualFold(v, "hex")
				cfg.traceHex = strings.EqualFold(v, "hex")
				traceSet = true
			case "trace_file":
				cfg.traceFile = v
			case "batch_rows":
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", p.name, v)
				}
				cfg.batchSize = n
			case "batch_bytes":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 0 {
					return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", p.name, v)
				}
				cfg.batchBytes = n
			case "user":
				cfg.drda.User = v
			case "password":
				cfg.drda.Password = v
			}
		}
	}

	for k, v := range opts {
		switch k {
		case OptionURI:
		case OptionUsername:
			cfg.drda.User = v
		case OptionPassword:
			cfg.drda.Password = v
		case OptionHost:
			cfg.drda.Host = v
		case OptionPort:
			p, err := strconv.Atoi(v)
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", k, v)
			}
			cfg.drda.Port = p
		case OptionDatabase:
			cfg.drda.Database = v
		case OptionTLS:
			tlsEnabled = isTrue(v)
		case OptionTLSCACert:
			caCert = v
		case OptionTLSSkipVerify:
			skipVerify = isTrue(v)
		case OptionSecurityMechanism:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", k, v)
			}
			cfg.drda.SecurityMechanism = uint16(n)
		case OptionCurrentSchema:
			cfg.drda.CurrentSchema = v
		case OptionQueryBlockSize:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", k, v)
			}
			cfg.drda.QueryBlockSize = uint32(n)
		case OptionConnectTimeout:
			d, err := parseTimeout(v)
			if err != nil {
				return nil, err
			}
			cfg.drda.ConnectTimeout = d
		case OptionApplicationName:
			cfg.drda.ApplicationName = v
		case OptionBatchSize:
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", k, v)
			}
			cfg.batchSize = n
		case OptionBatchBytes:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, errStatus(adbc.StatusInvalidArgument, "invalid %s %q", k, v)
			}
			cfg.batchBytes = n
		case OptionTrace:
			cfg.trace = isTrue(v) || strings.EqualFold(v, "hex")
			cfg.traceHex = strings.EqualFold(v, "hex")
			traceSet = true
		case OptionTraceFile:
			cfg.traceFile = v
		case OptionPackage:
			coll, pkg, ok := strings.Cut(v, ".")
			if !ok || coll == "" || pkg == "" {
				return nil, errStatus(adbc.StatusInvalidArgument, "%s must be COLLECTION.PKGID, got %q", k, v)
			}
			cfg.drda.PackageCollection, cfg.drda.PackageID = coll, pkg
		case OptionNoAutoBind:
			cfg.drda.NoAutoBind = isTrue(v)
		default:
			// Only standard connection/statement keys get this far (see
			// isDatabaseOption); they are ignored at the database level.
			if !isDatabaseOption(k) {
				return nil, errStatus(adbc.StatusNotImplemented, "unknown database option %q", k)
			}
		}
	}

	// A trace file turns tracing on unless trace was set explicitly (an
	// explicit db2.trace=false wins, whatever the order of the options).
	if !traceSet && cfg.traceFile != "" {
		cfg.trace = true
	}

	if cfg.drda.Host == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "no host given (set %s or %s)", OptionURI, OptionHost)
	}
	if cfg.drda.Database == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "no database given (URI path, e.g. db2://host:50000/SAMPLE)")
	}
	if cfg.drda.User == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "no user given (set %s)", OptionUsername)
	}
	if cfg.drda.Port == 0 {
		if tlsEnabled {
			cfg.drda.Port = 50001
		} else {
			cfg.drda.Port = 50000
		}
	}
	if tlsEnabled {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.drda.Host}
		if skipVerify {
			tc.InsecureSkipVerify = true //nolint:gosec // explicit user opt-in
		}
		if caCert != "" {
			pem, err := os.ReadFile(caCert)
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument, "read CA cert %q: %v", caCert, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errStatus(adbc.StatusInvalidArgument, "no certificates found in %q", caCert)
			}
			tc.RootCAs = pool
		}
		cfg.drda.TLS = tc
	}
	return cfg, nil
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func parseTimeout(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errStatus(adbc.StatusInvalidArgument, "invalid timeout %q: %v", v, err)
	}
	return d, nil
}

func errStatus(code adbc.Status, format string, args ...any) error {
	return adbc.Error{Code: code, Msg: driverMessage(fmt.Sprintf(format, args...))}
}

// databaseOptions are the keys a Database accepts.
var databaseOptions = map[string]bool{
	OptionURI: true, OptionUsername: true, OptionPassword: true,
	OptionHost: true, OptionPort: true, OptionDatabase: true,
	OptionTLS: true, OptionTLSCACert: true, OptionTLSSkipVerify: true,
	OptionSecurityMechanism: true, OptionCurrentSchema: true,
	OptionQueryBlockSize: true, OptionConnectTimeout: true,
	OptionApplicationName: true, OptionBatchSize: true, OptionBatchBytes: true,
	OptionTrace: true, OptionTraceFile: true, OptionPackage: true,
	OptionNoAutoBind: true,
}

// isDatabaseOption reports whether a Database accepts key. Besides its own
// options, it tolerates the standard adbc.connection.* / adbc.statement.*
// keys (ignored there), which generic tooling sometimes sets on the
// database; anything else is refused with NOT_IMPLEMENTED, as ADBC requires,
// so a misspelled option fails instead of being silently dropped.
func isDatabaseOption(key string) bool {
	if newKey, old := deprecatedOptions[key]; old {
		key = newKey
	}
	return databaseOptions[key] ||
		strings.HasPrefix(key, "adbc.connection.") ||
		strings.HasPrefix(key, "adbc.statement.")
}

// uriSettings maps each accepted URI query parameter (lower case) to the
// setting it controls. Several names for one setting are aliases.
var uriSettings = map[string]string{
	"tls": "tls", "ssl": "tls", "security": "tls",
	"tls_ca_cert": "tls_ca_cert", "sslcertificate": "tls_ca_cert", "ca_cert": "tls_ca_cert",
	"tls_skip_verify":    "tls_skip_verify",
	"secmec":             "security_mechanism",
	"security_mechanism": "security_mechanism",
	"securitymechanism":  "security_mechanism",
	"schema":             "current_schema", "current_schema": "current_schema", "currentschema": "current_schema",
	"query_block_size": "block_size", "block_size": "block_size", "queryblocksize": "block_size",
	"connect_timeout": "connect_timeout", "connect_timeout_seconds": "connect_timeout", "logintimeout": "connect_timeout",
	"application_name": "application_name", "applicationname": "application_name",
	"clientapplicationinformation": "application_name",
	"package":                      "package",
	"trace":                        "trace",
	"trace_file":                   "trace_file",
	"batch_size":                   "batch_rows", "batch_rows": "batch_rows",
	"batch_bytes": "batch_bytes",
	"user":        "user", "uid": "user",
	"password": "password", "pwd": "password",
}

// uriParam is one URI query parameter: the setting it controls, the name
// the URI used for it and its value.
type uriParam struct {
	setting, name, value string
}

// uriParameters parses a URI's query into one parameter per setting, in a
// fixed order (by setting name). A URI that gives one setting different
// values, under two names (tls and ssl), two spellings (TLS and tls) or
// twice under one name, is refused: which one wins would otherwise be up to
// map iteration order. The error names the parameters, never the values.
func uriParameters(rawQuery string) ([]uriParam, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, errStatus(adbc.StatusInvalidArgument, "could not parse the URI's query parameters: %s", uriProblem(err))
	}
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)
	bySetting := map[string]uriParam{}
	for _, name := range names {
		setting, ok := uriSettings[strings.ToLower(name)]
		if !ok {
			return nil, errStatus(adbc.StatusInvalidArgument, "unknown URI parameter %q", name)
		}
		for _, v := range q[name] {
			prev, seen := bySetting[setting]
			if !seen {
				bySetting[setting] = uriParam{setting: setting, name: name, value: v}
				continue
			}
			if prev.value != v {
				if prev.name == name {
					return nil, errStatus(adbc.StatusInvalidArgument,
						"URI parameter %q is given more than once with different values", name)
				}
				return nil, errStatus(adbc.StatusInvalidArgument,
					"URI parameters %q and %q set the same thing with different values; give one", prev.name, name)
			}
		}
	}
	settings := make([]string, 0, len(bySetting))
	for setting := range bySetting {
		settings = append(settings, setting)
	}
	sort.Strings(settings)
	out := make([]uriParam, 0, len(settings))
	for _, setting := range settings {
		out = append(out, bySetting[setting])
	}
	return out, nil
}

// uriProblem describes why a URI did not parse without quoting any of it:
// the offending text can be part of a password.
func uriProblem(err error) string {
	var escapeErr url.EscapeError
	if errors.As(err, &escapeErr) {
		return "a \"%\" is not followed by two hex digits (percent-encode a \"%\" in a user name, password or parameter as %25)"
	}
	var hostErr url.InvalidHostError
	if errors.As(err, &hostErr) {
		return "invalid character in the host (percent-encode any \"/\", \"@\", \":\" or \"%\" in the user name or password)"
	}
	return "malformed URI (percent-encode special characters in the user name or password)"
}

// redactedURI returns the URI with any password (in the user info, or a
// password=/pwd= query parameter) replaced by "xxxxx". A URI that does not
// parse is not returned at all.
func redactedURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errStatus(adbc.StatusInvalidState, "the URI could not be parsed, so it is not returned (it may hold a password)")
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", errStatus(adbc.StatusInvalidState, "the URI's query could not be parsed, so it is not returned (it may hold a password)")
		}
		changed := false
		for name := range q {
			if setting := uriSettings[strings.ToLower(name)]; setting == "password" {
				for i := range q[name] {
					q[name][i] = "xxxxx"
				}
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.Redacted(), nil
}
