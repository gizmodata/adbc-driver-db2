// Package db2 implements an Apache Arrow ADBC driver for IBM Db2 over
// the DRDA wire protocol, in pure Go (no IBM CLI / ODBC dependency).
package db2

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// ADBC option keys. They follow the `adbc.<vendor>.<noun>` convention.
const (
	// OptionURI is "adbc.uri": db2://[user[:password]@]host[:port]/DATABASE[?params]
	OptionURI      = adbc.OptionKeyURI
	OptionUsername = adbc.OptionKeyUsername // "username"
	OptionPassword = adbc.OptionKeyPassword // "password"

	OptionHost     = "adbc.db2.host"
	OptionPort     = "adbc.db2.port"
	OptionDatabase = "adbc.db2.database"
	// OptionTLS enables TLS ("true"/"false"). Db2's SSL port is 50001
	// by convention; IBM Db2 on Cloud requires TLS.
	OptionTLS = "adbc.db2.tls"
	// OptionTLSCACert is a path to a PEM CA bundle used to verify the
	// server (for self-signed Db2 certificates).
	OptionTLSCACert = "adbc.db2.tls.ca_cert"
	// OptionTLSSkipVerify disables server certificate verification.
	OptionTLSSkipVerify = "adbc.db2.tls.skip_verify"
	// OptionSecurityMechanism forces a DRDA SECMEC: "3" (cleartext
	// password), "4" (user id only), "9" (Diffie-Hellman encrypted;
	// default when the server allows it).
	OptionSecurityMechanism = "adbc.db2.security_mechanism"
	// OptionCurrentSchema runs SET CURRENT SCHEMA after connecting.
	OptionCurrentSchema = "adbc.db2.current_schema"
	// OptionQueryBlockSize is the DRDA QRYBLKSZ in bytes (default 1 MiB;
	// larger blocks mean fewer round trips per result set).
	OptionQueryBlockSize = "adbc.db2.query_block_size"
	// OptionConnectTimeout: plain digits are seconds, else a Go duration.
	OptionConnectTimeout = "adbc.db2.connect_timeout"
	// OptionApplicationName is reported to the server as the DRDA
	// external name (visible in LIST APPLICATIONS / MON_GET_CONNECTION).
	OptionApplicationName = "adbc.db2.application_name"
	// OptionBatchSize caps rows per Arrow record batch (default 65536).
	OptionBatchSize = "adbc.db2.batch_size"
	// OptionBatchBytes caps the approximate size of an Arrow record batch
	// in bytes (0 = unlimited; rows are still capped by batch_size).
	OptionBatchBytes = "adbc.db2.batch_bytes"
	// OptionTrace: "true" logs DRDA traffic (one line per message plus the
	// SQL text); "hex" additionally dumps reply payloads. Goes to stderr
	// unless OptionTraceFile names a file (appended to) — use that from
	// notebooks, whose cells do not show the process's stderr.
	OptionTrace     = "adbc.db2.trace"
	OptionTraceFile = "adbc.db2.trace_file"
	// OptionPackage names the dynamic-SQL package as COLLECTION.PKGID
	// (default NULLID.SYSSH200). If the package does not exist on the
	// server (SQL0805N — typical on Db2 for i / z/OS, which do not ship
	// the CLI packages), the driver binds it the way IBM's DB2Binder
	// does; OptionNoAutoBind ("true") disables that.
	OptionPackage    = "adbc.db2.package"
	OptionNoAutoBind = "adbc.db2.no_auto_bind"

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
	cfg := &connConfig{batchSize: 65536}
	cfg.drda.ConnectTimeout = 30 * time.Second
	var tlsEnabled, skipVerify bool
	var caCert string

	if raw := opts[OptionURI]; raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid URI %q: %v", raw, err)
		}
		switch u.Scheme {
		case "db2", "drda", "ibm-db2", "db2s":
		default:
			return nil, errStatus(adbc.StatusInvalidArgument, "db2: unsupported URI scheme %q (want db2://)", u.Scheme)
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
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid port %q", port)
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
		q := u.Query()
		for key, vals := range q {
			if len(vals) == 0 {
				continue
			}
			v := vals[0]
			switch strings.ToLower(key) {
			case "tls", "ssl", "security":
				tlsEnabled = isTrue(v) || strings.EqualFold(v, "ssl")
			case "tls_ca_cert", "sslcertificate", "ca_cert":
				caCert = v
			case "tls_skip_verify":
				skipVerify = isTrue(v)
			case "secmec", "security_mechanism", "securitymechanism":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid security mechanism %q", v)
				}
				cfg.drda.SecurityMechanism = uint16(n)
			case "schema", "current_schema", "currentschema":
				cfg.drda.CurrentSchema = v
			case "query_block_size", "queryblocksize":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid query_block_size %q", v)
				}
				cfg.drda.QueryBlockSize = uint32(n)
			case "connect_timeout", "logintimeout":
				d, err := parseTimeout(v)
				if err != nil {
					return nil, err
				}
				cfg.drda.ConnectTimeout = d
			case "application_name", "applicationname", "clientapplicationinformation":
				cfg.drda.ApplicationName = v
			case "package":
				coll, pkg, ok := strings.Cut(v, ".")
				if !ok || coll == "" || pkg == "" {
					return nil, errStatus(adbc.StatusInvalidArgument, "db2: package must be COLLECTION.PKGID, got %q", v)
				}
				cfg.drda.PackageCollection, cfg.drda.PackageID = coll, pkg
			case "trace":
				cfg.trace = isTrue(v) || strings.EqualFold(v, "hex")
				cfg.traceHex = strings.EqualFold(v, "hex")
			case "trace_file":
				cfg.traceFile = v
				cfg.trace = true
			case "batch_size":
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid batch_size %q", v)
				}
				cfg.batchSize = n
			case "batch_bytes":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 0 {
					return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid batch_bytes %q", v)
				}
				cfg.batchBytes = n
			case "user", "uid":
				cfg.drda.User = v
			case "password", "pwd":
				cfg.drda.Password = v
			default:
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: unknown URI parameter %q", key)
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
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid %s %q", k, v)
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
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid %s %q", k, v)
			}
			cfg.drda.SecurityMechanism = uint16(n)
		case OptionCurrentSchema:
			cfg.drda.CurrentSchema = v
		case OptionQueryBlockSize:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid %s %q", k, v)
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
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid %s %q", k, v)
			}
			cfg.batchSize = n
		case OptionBatchBytes:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: invalid %s %q", k, v)
			}
			cfg.batchBytes = n
		case OptionTrace:
			cfg.trace = isTrue(v) || strings.EqualFold(v, "hex")
			cfg.traceHex = strings.EqualFold(v, "hex")
		case OptionTraceFile:
			cfg.traceFile = v
			cfg.trace = true
		case OptionPackage:
			coll, pkg, ok := strings.Cut(v, ".")
			if !ok || coll == "" || pkg == "" {
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: %s must be COLLECTION.PKGID, got %q", k, v)
			}
			cfg.drda.PackageCollection, cfg.drda.PackageID = coll, pkg
		case OptionNoAutoBind:
			cfg.drda.NoAutoBind = isTrue(v)
		default:
			// Unknown options are ignored so generic tooling that sets
			// e.g. adbc.connection.autocommit at the database level
			// doesn't fail.
		}
	}

	if cfg.drda.Host == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "db2: no host given (set %s or %s)", OptionURI, OptionHost)
	}
	if cfg.drda.Database == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "db2: no database given (URI path, e.g. db2://host:50000/SAMPLE)")
	}
	if cfg.drda.User == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "db2: no user given (set %s)", OptionUsername)
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
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: read CA cert %q: %v", caCert, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: no certificates found in %q", caCert)
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
		return 0, errStatus(adbc.StatusInvalidArgument, "db2: invalid timeout %q: %v", v, err)
	}
	return d, nil
}

func errStatus(code adbc.Status, format string, args ...any) error {
	return adbc.Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}
