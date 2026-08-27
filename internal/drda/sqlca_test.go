package drda

import (
	"encoding/binary"
	"testing"

	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
	"github.com/stretchr/testify/require"
)

// buildSQLCARD assembles a minimal SQLCARD body with the given SQLCODE,
// SQLSTATE/SQLERRP bytes, and message.
func buildSQLCARD(sqlcode int32, sqlstate, sqlerrp []byte, msg []byte, bigEndian bool) []byte {
	b := []byte{0x00}
	if bigEndian {
		b = binary.BigEndian.AppendUint32(b, uint32(sqlcode))
	} else {
		b = binary.LittleEndian.AppendUint32(b, uint32(sqlcode))
	}
	b = append(b, sqlstate...)
	b = append(b, sqlerrp...)
	b = append(b, 0x00) // SQLCAXGRP present
	b = append(b, make([]byte, 24)...)
	b = append(b, make([]byte, 11)...)
	b = append(b, 0x00, 0x00) // rdbname
	b = binary.BigEndian.AppendUint16(b, uint16(len(msg)))
	b = append(b, msg...)
	b = append(b, 0x00, 0x00) // msg_s
	b = append(b, 0xFF)       // SQLDIAGGRP null
	return b
}

func TestParseSQLCARD_ASCII(t *testing.T) {
	body := buildSQLCARD(-204, []byte("42704"), []byte("SQL12015"), []byte("DB2INST1.T"), false)
	ca, err := ParseSQLCARD(body, true)
	require.NoError(t, err)
	require.Equal(t, int32(-204), ca.SQLCode)
	require.Equal(t, "42704", ca.SQLState)
	require.Equal(t, "SQL12015", ca.SQLErrp)
	require.Equal(t, "DB2INST1.T", ca.Message)
}

// Db2 for z/OS and Db2 for i flow SQLCA character fields in EBCDIC with
// big-endian integers.
func TestParseSQLCARD_EBCDIC(t *testing.T) {
	body := buildSQLCARD(-104, ddm.EncodeEBCDIC("42601"), ddm.PadEBCDIC("QSQPARSE", 8),
		ddm.EncodeEBCDIC("LIMIT"), true)
	ca, err := ParseSQLCARD(body, false)
	require.NoError(t, err)
	require.Equal(t, int32(-104), ca.SQLCode)
	require.Equal(t, "42601", ca.SQLState)
	require.Equal(t, "QSQPARSE", ca.SQLErrp)
	require.Equal(t, "LIMIT", ca.Message)
	for _, c := range ca.SQLState {
		require.Less(t, c, rune(0x80))
	}
}

func TestExcsatrdAgreesCCSID1208(t *testing.T) {
	// Db2 LUW's reply: MGRLVLLS with CCSIDMGR 1208.
	require.True(t, excsatrdAgreesCCSID1208([]byte{0x00, 0x08, 0x14, 0x04, 0x14, 0xCC, 0x04, 0xB8}))
	// A server answering with a lower CCSIDMGR level, or none.
	require.False(t, excsatrdAgreesCCSID1208([]byte{0x00, 0x08, 0x14, 0x04, 0x14, 0xCC, 0x00, 0x01}))
	require.False(t, excsatrdAgreesCCSID1208([]byte{0x00, 0x08, 0x14, 0x04, 0x14, 0x03, 0x00, 0x0A}))
	require.False(t, excsatrdAgreesCCSID1208(nil))
}
