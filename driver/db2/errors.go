package db2

import (
	"context"
	"errors"
	"net"

	"github.com/apache/arrow-adbc/go/adbc"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// fromDRDAError maps DRDA-layer errors onto ADBC statuses, preserving
// SQLSTATE / SQLCODE where Db2 supplied them.
func fromDRDAError(err error) error {
	if err == nil {
		return nil
	}
	var ae adbc.Error
	if errors.As(err, &ae) {
		return err
	}
	var ca *drda.SQLCA
	if errors.As(err, &ca) {
		e := adbc.Error{Code: statusForSQLCA(ca), Msg: "db2: " + ca.Error(), VendorCode: ca.SQLCode}
		copy(e.SqlState[:], ca.SQLState)
		return e
	}
	var auth *drda.AuthError
	if errors.As(err, &auth) {
		return adbc.Error{Code: adbc.StatusUnauthenticated, Msg: auth.Error()}
	}
	var pe *drda.ProtocolError
	if errors.As(err, &pe) {
		return adbc.Error{Code: adbc.StatusInternal, Msg: pe.Error()}
	}
	if errors.Is(err, context.Canceled) {
		return adbc.Error{Code: adbc.StatusCancelled, Msg: err.Error()}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return adbc.Error{Code: adbc.StatusTimeout, Msg: err.Error()}
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return adbc.Error{Code: adbc.StatusTimeout, Msg: err.Error()}
		}
		return adbc.Error{Code: adbc.StatusIO, Msg: err.Error()}
	}
	return adbc.Error{Code: adbc.StatusIO, Msg: err.Error()}
}

func statusForSQLCA(ca *drda.SQLCA) adbc.Status {
	switch ca.SQLCode {
	case -204, -206, -205: // object / column not found
		return adbc.StatusNotFound
	case -104, -199, -7: // syntax
		return adbc.StatusInvalidArgument
	case -551, -552: // not authorized
		return adbc.StatusUnauthorized
	case -601: // already exists
		return adbc.StatusAlreadyExists
	case -803: // duplicate key
		return adbc.StatusIntegrity
	case -911, -913: // deadlock / timeout
		return adbc.StatusTimeout
	case -30081, -30080: // communication error
		return adbc.StatusIO
	}
	if len(ca.SQLState) >= 2 {
		switch ca.SQLState[:2] {
		case "42":
			return adbc.StatusInvalidArgument
		case "23":
			return adbc.StatusIntegrity
		case "08":
			return adbc.StatusIO
		case "28":
			return adbc.StatusUnauthenticated
		}
	}
	return adbc.StatusInternal
}
