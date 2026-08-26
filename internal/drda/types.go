package drda

// FD:OCA DRDA data types as they appear in QRYDSC / FDODSC triplets.
// Odd values are the nullable variants of the preceding even value.
const (
	TypeInteger      byte = 0x02
	TypeNInteger     byte = 0x03
	TypeSmall        byte = 0x04
	TypeNSmall       byte = 0x05
	Type1ByteInt     byte = 0x06
	TypeN1ByteInt    byte = 0x07
	TypeFloat16      byte = 0x08
	TypeNFloat16     byte = 0x09
	TypeFloat8       byte = 0x0A
	TypeNFloat8      byte = 0x0B
	TypeFloat4       byte = 0x0C
	TypeNFloat4      byte = 0x0D
	TypeDecimal      byte = 0x0E
	TypeNDecimal     byte = 0x0F
	TypeZDecimal     byte = 0x10
	TypeNZDecimal    byte = 0x11
	TypeNumericChar  byte = 0x12
	TypeNNumericChar byte = 0x13
	TypeRsetLoc      byte = 0x14
	TypeNRsetLoc     byte = 0x15
	TypeInteger8     byte = 0x16
	TypeNInteger8    byte = 0x17
	TypeLobLoc       byte = 0x18
	TypeNLobLoc      byte = 0x19
	TypeClobLoc      byte = 0x1A
	TypeNClobLoc     byte = 0x1B
	TypeDbcsClobLoc  byte = 0x1C
	TypeNDbcsClobLoc byte = 0x1D
	TypeRowID        byte = 0x1E
	TypeNRowID       byte = 0x1F
	TypeDate         byte = 0x20
	TypeNDate        byte = 0x21
	TypeTime         byte = 0x22
	TypeNTime        byte = 0x23
	TypeTimestamp    byte = 0x24
	TypeNTimestamp   byte = 0x25
	TypeFixByte      byte = 0x26
	TypeNFixByte     byte = 0x27
	TypeVarByte      byte = 0x28
	TypeNVarByte     byte = 0x29
	TypeLongVarByte  byte = 0x2A
	TypeNLongVarByte byte = 0x2B
	TypeNTermByte    byte = 0x2C
	TypeNNTermByte   byte = 0x2D
	TypeCStr         byte = 0x2E
	TypeNCStr        byte = 0x2F
	TypeChar         byte = 0x30
	TypeNChar        byte = 0x31
	TypeVarChar      byte = 0x32
	TypeNVarChar     byte = 0x33
	TypeLong         byte = 0x34
	TypeNLong        byte = 0x35
	TypeGraphic      byte = 0x36
	TypeNGraphic     byte = 0x37
	TypeVarGraph     byte = 0x38
	TypeNVarGraph    byte = 0x39
	TypeLongGraph    byte = 0x3A
	TypeNLongGraph   byte = 0x3B
	TypeMix          byte = 0x3C
	TypeNMix         byte = 0x3D
	TypeVarMix       byte = 0x3E
	TypeNVarMix      byte = 0x3F
	TypeLongMix      byte = 0x40
	TypeNLongMix     byte = 0x41
	TypeCStrMix      byte = 0x42
	TypeNCStrMix     byte = 0x43
	TypePsclByte     byte = 0x44
	TypeNPsclByte    byte = 0x45
	TypeLStr         byte = 0x46
	TypeNLStr        byte = 0x47
	TypeLStrMix      byte = 0x48
	TypeNLStrMix     byte = 0x49
	TypeSDatalink    byte = 0x4C
	TypeNSDatalink   byte = 0x4D
	TypeMDatalink    byte = 0x4E
	TypeNMDatalink   byte = 0x4F
	TypeDecFloat     byte = 0xBA
	TypeNDecFloat    byte = 0xBB
	TypeBoolean      byte = 0xBE
	TypeNBoolean     byte = 0xBF
	// Db2-specific inline forms.
	TypeFixBytes   byte = 0xC0
	TypeNFixBytes  byte = 0xC1
	TypeVarBinary  byte = 0xC2
	TypeNVarBinary byte = 0xC3
	TypeLobBytes   byte = 0xC8 // inline BLOB, data arrives via EXTDTA
	TypeNLobBytes  byte = 0xC9
	TypeLobCSBCS   byte = 0xCE // inline CLOB (single-byte)
	TypeNLobCSBCS  byte = 0xCF
)

// IsNullable reports whether a DRDA FD:OCA type is the nullable variant.
func IsNullable(t byte) bool { return t&1 == 1 }

// Db2 SQLTYPE codes as they appear in SQLDA descriptors (SQLDARD).
// Odd = nullable.
const (
	SQLTypeDate          uint16 = 384
	SQLTypeTime          uint16 = 388
	SQLTypeTimestamp     uint16 = 392
	SQLTypeDatalink      uint16 = 396
	SQLTypeBlob          uint16 = 404
	SQLTypeClob          uint16 = 408
	SQLTypeDBClob        uint16 = 412
	SQLTypeVarchar       uint16 = 448
	SQLTypeChar          uint16 = 452
	SQLTypeLongVarchar   uint16 = 456
	SQLTypeCStr          uint16 = 460
	SQLTypeVargraphic    uint16 = 464
	SQLTypeGraphic       uint16 = 468
	SQLTypeLongVargraph  uint16 = 472
	SQLTypeLStr          uint16 = 476
	SQLTypeFloat         uint16 = 480
	SQLTypeDecimal       uint16 = 484
	SQLTypeZoned         uint16 = 488
	SQLTypeBigint        uint16 = 492
	SQLTypeInteger       uint16 = 496
	SQLTypeSmallint      uint16 = 500
	SQLTypeNumeric       uint16 = 504
	SQLTypeRowID         uint16 = 904
	SQLTypeVarbinary     uint16 = 908
	SQLTypeBinary        uint16 = 912
	SQLTypeBlobLocator   uint16 = 960
	SQLTypeClobLocator   uint16 = 964
	SQLTypeDBClobLocator uint16 = 968
	SQLTypeXML           uint16 = 988
	SQLTypeDecFloat      uint16 = 996
	SQLTypeBoolean       uint16 = 2436
)

// BaseSQLType strips the nullable bit from a Db2 SQLTYPE.
func BaseSQLType(t uint16) uint16 { return t &^ 1 }

// SQLTypeNullable reports whether the SQLTYPE is the nullable variant.
func SQLTypeNullable(t uint16) bool { return t&1 == 1 }
