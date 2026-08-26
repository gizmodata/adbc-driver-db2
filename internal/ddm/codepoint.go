// Package ddm implements the Distributed Data Management (DDM) layer of
// DRDA: DSS (Data Stream Structure) framing, DDM object encoding, code
// points, and the EBCDIC (CCSID 500) codec used for DDM-level strings.
//
// References: The Open Group "DRDA V5 Vol. 2: Distributed Data Management
// Architecture"; Apache Derby org.apache.derby.client.net.CodePoint.
package ddm

// CodePoint is a 2-byte DDM code point identifying a command, reply
// message, or parameter object.
type CodePoint uint16

// Commands (requests).
const (
	EXCSAT    CodePoint = 0x1041 // Exchange Server Attributes
	SYNCCTL   CodePoint = 0x1055
	SYNCRSY   CodePoint = 0x1069
	ACCSEC    CodePoint = 0x106D // Access Security
	SECCHK    CodePoint = 0x106E // Security Check
	SYNCLOG   CodePoint = 0x106F
	ACCRDB    CodePoint = 0x2001 // Access RDB
	BGNBND    CodePoint = 0x2002
	BNDSQLSTT CodePoint = 0x2004
	CLSQRY    CodePoint = 0x2005 // Close Query
	CNTQRY    CodePoint = 0x2006 // Continue Query
	DRPPKG    CodePoint = 0x2007
	DSCSQLSTT CodePoint = 0x2008 // Describe SQL Statement
	ENDBND    CodePoint = 0x2009
	EXCSQLIMM CodePoint = 0x200A // Execute Immediate SQL Statement
	EXCSQLSTT CodePoint = 0x200B // Execute SQL Statement
	OPNQRY    CodePoint = 0x200C // Open Query
	PRPSQLSTT CodePoint = 0x200D // Prepare SQL Statement
	RDBCMM    CodePoint = 0x200E // RDB Commit Unit of Work
	RDBRLLBCK CodePoint = 0x200F // RDB Rollback Unit of Work
	REBIND    CodePoint = 0x2010
	DSCRDBTBL CodePoint = 0x2012
	EXCSQLSET CodePoint = 0x2014 // Set SQL Environment
)

// Command data objects.
const (
	SQLDTA    CodePoint = 0x2412 // SQL Program Variable Data
	SQLDTARD  CodePoint = 0x2413 // SQL Data Reply Data
	SQLSTT    CodePoint = 0x2414 // SQL Statement
	SQLATTR   CodePoint = 0x2450 // SQL Statement Attributes
	SQLSTTVRB CodePoint = 0x2419
	QRYDSC    CodePoint = 0x241A // Query Answer Set Description
	QRYDTA    CodePoint = 0x241B // Query Answer Set Data
	SQLRSLRD  CodePoint = 0x240E
	SQLCINRD  CodePoint = 0x240B
	SQLCARD   CodePoint = 0x2408 // SQL Communications Area Reply Data
	SQLDARD   CodePoint = 0x2411 // SQL Descriptor Area Reply Data
	EXTDTA    CodePoint = 0x146C // Externalized Data (LOBs)
	FDODSC    CodePoint = 0x0010 // FD:OCA Data Descriptor
	FDODTA    CodePoint = 0x147A // FD:OCA Data
	FDODSCOFF CodePoint = 0x2118
	FDOPRMOFF CodePoint = 0x212B
	FDOTRPOFF CodePoint = 0x212A
)

// Reply messages.
const (
	EXCSATRD CodePoint = 0x1443 // Server Attributes Reply Data
	ACCSECRD CodePoint = 0x14AC // Access Security Reply Data
	SECCHKRM CodePoint = 0x1219 // Security Check Reply Message
	ACCRDBRM CodePoint = 0x2201 // Access RDB Reply Message
	OPNQRYRM CodePoint = 0x2205 // Open Query Complete
	ENDQRYRM CodePoint = 0x220B // End of Query
	ENDUOWRM CodePoint = 0x220C // End Unit of Work
	SQLERRRM CodePoint = 0x2213 // SQL Error Condition
	QRYNOPRM CodePoint = 0x2202 // Query Not Open
	QRYPOPRM CodePoint = 0x220F // Query Previously Opened
	RDBNFNRM CodePoint = 0x2211 // RDB Not Found
	RDBATHRM CodePoint = 0x22CB // Not Authorized to RDB
	RDBNACRM CodePoint = 0x2204 // RDB Not Accessed
	RDBACCRM CodePoint = 0x2207 // RDB Currently Accessed
	RDBAFLRM CodePoint = 0x221A // RDB Access Failed
	RDBUPDRM CodePoint = 0x2218
	ABNUOWRM CodePoint = 0x220D // Abnormal End of Unit of Work
	CMDATHRM CodePoint = 0x121C // Not Authorized to Command
	CMDCHKRM CodePoint = 0x1254 // Command Check
	CMDNSPRM CodePoint = 0x1250 // Command Not Supported
	CMDCMPRM CodePoint = 0x124B
	CMDVLTRM CodePoint = 0x221D
	CMMRQSRM CodePoint = 0x2225
	AGNPRMRM CodePoint = 0x1232 // Permanent Agent Error
	BGNBNDRM CodePoint = 0x2208
	MGRLVLRM CodePoint = 0x1210 // Manager-Level Conflict
	MGRDEPRM CodePoint = 0x1218
	OBJNSPRM CodePoint = 0x1253 // Object Not Supported
	PRCCNVRM CodePoint = 0x1245 // Conversational Protocol Error
	PRMNSPRM CodePoint = 0x1251 // Parameter Not Supported
	PKGBNARM CodePoint = 0x2206
	PKGBPARM CodePoint = 0x2209
	RSCLMTRM CodePoint = 0x1233 // Resource Limits Reached
	SYNTAXRM CodePoint = 0x124C // Data Stream Syntax Error
	TRGNSPRM CodePoint = 0x125F
	VALNSPRM CodePoint = 0x1252 // Parameter Value Not Supported
	DTAMCHRM CodePoint = 0x220E // Data Descriptor Mismatch
	OPNQFLRM CodePoint = 0x2212 // Open Query Failure
	RSLSETRM CodePoint = 0x2219
	DSCINVRM CodePoint = 0x220A
	SYNCCRD  CodePoint = 0x1248
	SYNCRRD  CodePoint = 0x126D
)

// Parameters / instance variables.
const (
	AGENT       CodePoint = 0x1403
	CODPNT      CodePoint = 0x000C
	CODPNTDR    CodePoint = 0x0064
	CSTMBCS     CodePoint = 0x2435
	CCSIDDBC    CodePoint = 0x119D
	CCSIDMBC    CodePoint = 0x119E
	CCSIDMGR    CodePoint = 0x14CC
	UNICODEMGR  CodePoint = 0x1C08
	CCSIDSBC    CodePoint = 0x119C
	CMNAPPC     CodePoint = 0x1444
	CMNSYNCPT   CodePoint = 0x147C
	CMNTCPIP    CodePoint = 0x1474
	XAMGR       CodePoint = 0x1C01
	CRRTKN      CodePoint = 0x2135 // Correlation Token
	TRGDFTRT    CodePoint = 0x213B
	FREPRVREF   CodePoint = 0x214C
	DICTIONARY  CodePoint = 0x1458
	DEPERRCD    CodePoint = 0x119B
	DSCERRCD    CodePoint = 0x2101
	EXTNAM      CodePoint = 0x115E // External Name
	FIXROWPRC   CodePoint = 0x2418
	FRCFIXROW   CodePoint = 0x2410
	LMTBLKPRC   CodePoint = 0x2417
	MGRLVLLS    CodePoint = 0x1404 // Manager-Level List
	MGRLVLN     CodePoint = 0x1473
	MONITOR     CodePoint = 0x1900
	MONITORRD   CodePoint = 0x1C00
	NEWPASSWORD CodePoint = 0x11DE
	PASSWORD    CodePoint = 0x11A1
	PKGDFTCST   CodePoint = 0x2125
	PKGID       CodePoint = 0x2109
	MAXBLKEXT   CodePoint = 0x2141
	MAXRSLCNT   CodePoint = 0x2140
	RSLSETFLG   CodePoint = 0x2142
	RDBCMTOK    CodePoint = 0x2105 // RDB Commit Allowed
	PKGNAMCT    CodePoint = 0x2112
	PKGSNLST    CodePoint = 0x2139
	PRCCNVCD    CodePoint = 0x113F
	PRDID       CodePoint = 0x112E // Product-Specific Identifier
	OUTOVR      CodePoint = 0x2415
	OUTOVROPT   CodePoint = 0x2147
	PKGCNSTKN   CodePoint = 0x210D
	PRDDTA      CodePoint = 0x2104
	QRYINSID    CodePoint = 0x215B // Query Instance Identifier
	QRYBLKCTL   CodePoint = 0x2132
	QRYBLKSZ    CodePoint = 0x2114 // Query Block Size
	QRYPRCTYP   CodePoint = 0x2102
	QRYCLSIMP   CodePoint = 0x215D // Query Close Implicit
	QRYCLSRLS   CodePoint = 0x215E
	QRYOPTVAL   CodePoint = 0x215F
	NBRROW      CodePoint = 0x213A
	OUTEXP      CodePoint = 0x2111
	PRCNAM      CodePoint = 0x2138
	QRYATTUPD   CodePoint = 0x2150
	RDB         CodePoint = 0x240F
	RDBACCCL    CodePoint = 0x210F // RDB Access Manager Class
	RDBALWUPD   CodePoint = 0x211A
	QRYRELSCR   CodePoint = 0x213C
	QRYSCRORN   CodePoint = 0x2152
	QRYROWNBR   CodePoint = 0x213D
	QRYROWSNS   CodePoint = 0x2153
	QRYRFRTBL   CodePoint = 0x213E
	QRYATTSCR   CodePoint = 0x2149
	QRYATTSNS   CodePoint = 0x2157
	QRYBLKRST   CodePoint = 0x2154
	QRYROWSET   CodePoint = 0x2156
	QRYRTNDTA   CodePoint = 0x2155
	RDBINTTKN   CodePoint = 0x2103
	RDBNAM      CodePoint = 0x2110 // Relational Database Name
	RDBCOLID    CodePoint = 0x2108
	RSCNAM      CodePoint = 0x112D
	RSCTYP      CodePoint = 0x111F
	RSNCOD      CodePoint = 0x1127
	RSYNCMGR    CodePoint = 0x14C1
	RTNSQLDA    CodePoint = 0x2116 // Return SQL Descriptor Area
	TYPSQLDA    CodePoint = 0x2146 // Type of SQL Descriptor Area
	SECCHKCD    CodePoint = 0x11A4 // Security Check Code
	SECMEC      CodePoint = 0x11A2 // Security Mechanism
	SECMGR      CodePoint = 0x1440
	SECMGRNM    CodePoint = 0x1196
	SECTKN      CodePoint = 0x11DC // Security Token
	RTNEXTDTA   CodePoint = 0x2148 // Return of EXTDTA Option
	SPVNAM      CodePoint = 0x115D
	SQLAM       CodePoint = 0x2407
	SQLCSRHLD   CodePoint = 0x211F
	SRVCLSNM    CodePoint = 0x1147 // Server Class Name
	SRVDGN      CodePoint = 0x1153 // Server Diagnostic Information
	SRVLST      CodePoint = 0x244E
	SRVNAM      CodePoint = 0x116D // Server Name
	SRVRLSLV    CodePoint = 0x115A // Server Product Release Level
	STTDECDEL   CodePoint = 0x2121
	STTSTRDEL   CodePoint = 0x2120
	SUPERVISOR  CodePoint = 0x143C
	SVCERRNO    CodePoint = 0x11B4
	SVRCOD      CodePoint = 0x1149 // Severity Code
	SYNCPTMGR   CodePoint = 0x14C0
	SYNERRCD    CodePoint = 0x114A // Syntax Error Code
	TYPDEFNAM   CodePoint = 0x002F // Data Type Definition Name
	TYPDEFOVR   CodePoint = 0x0035 // TYPDEF Overrides
	UOWDSP      CodePoint = 0x2115 // Unit of Work Disposition
	USRID       CodePoint = 0x11A0 // User ID
	VRSNAM      CodePoint = 0x1144
	PKGNAMCSN   CodePoint = 0x2113 // RDB Package Name, Consistency Token, and Section Number
	DIAGLVL     CodePoint = 0x2160
	PBSD        CodePoint = 0xC000
	PBSD_ISO    CodePoint = 0xC001
	PBSD_SCHEMA CodePoint = 0xC002
	RLSCONV     CodePoint = 0x119F
	XARETVAL    CodePoint = 0x1904
	TIMEOUT     CodePoint = 0x1907
	FORGET      CodePoint = 0x1186
	SYNCTYPE    CodePoint = 0x1187
	XID         CodePoint = 0x1801
	XAFLAGS     CodePoint = 0x1903
	RSYNCTYP    CodePoint = 0x11EA
	PRPHRCLST   CodePoint = 0x1905
	XIDCNT      CodePoint = 0x1906
	RDBRLLBCK2  CodePoint = 0xC004
	DYNDTAFMT   CodePoint = 0x214B // Dynamic Data Format
)

// Security mechanisms (SECMEC values).
const (
	SecMecDCESEC      uint16 = 1
	SecMecUSRIDPWD    uint16 = 3 // user id + cleartext password
	SecMecUSRIDONL    uint16 = 4 // user id only
	SecMecUSRIDNWPWD  uint16 = 5
	SecMecUSRSBSPWD   uint16 = 6
	SecMecUSRENCPWD   uint16 = 7 // user id + DES-encrypted password
	SecMecUSRSSBPWD   uint16 = 8
	SecMecEUSRIDPWD   uint16 = 9 // DH-encrypted user id + password
	SecMecEUSRIDNWPWD uint16 = 10
)

// Severity codes (SVRCOD).
const (
	SvrCodInfo    uint16 = 0
	SvrCodWarning uint16 = 4
	SvrCodError   uint16 = 8
	SvrCodSevere  uint16 = 16
	SvrCodAccDmg  uint16 = 32
	SvrCodPrmDmg  uint16 = 64
	SvrCodSesDmg  uint16 = 128
)

// String returns the DDM mnemonic for a code point when known.
func (c CodePoint) String() string {
	if s, ok := codePointNames[c]; ok {
		return s
	}
	return "0x" + hex16(uint16(c))
}

func hex16(v uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[v>>12&0xF], digits[v>>8&0xF], digits[v>>4&0xF], digits[v&0xF]})
}

var codePointNames = map[CodePoint]string{
	EXCSAT: "EXCSAT", ACCSEC: "ACCSEC", SECCHK: "SECCHK", ACCRDB: "ACCRDB",
	CLSQRY: "CLSQRY", CNTQRY: "CNTQRY", DSCSQLSTT: "DSCSQLSTT", EXCSQLIMM: "EXCSQLIMM",
	EXCSQLSTT: "EXCSQLSTT", OPNQRY: "OPNQRY", PRPSQLSTT: "PRPSQLSTT", RDBCMM: "RDBCMM",
	RDBRLLBCK: "RDBRLLBCK", EXCSQLSET: "EXCSQLSET",
	SQLDTA: "SQLDTA", SQLDTARD: "SQLDTARD", SQLSTT: "SQLSTT", SQLATTR: "SQLATTR",
	QRYDSC: "QRYDSC", QRYDTA: "QRYDTA", SQLCARD: "SQLCARD", SQLDARD: "SQLDARD",
	EXTDTA: "EXTDTA", FDODSC: "FDODSC", FDODTA: "FDODTA",
	EXCSATRD: "EXCSATRD", ACCSECRD: "ACCSECRD", SECCHKRM: "SECCHKRM", ACCRDBRM: "ACCRDBRM",
	OPNQRYRM: "OPNQRYRM", ENDQRYRM: "ENDQRYRM", ENDUOWRM: "ENDUOWRM", SQLERRRM: "SQLERRRM",
	QRYNOPRM: "QRYNOPRM", QRYPOPRM: "QRYPOPRM", RDBNFNRM: "RDBNFNRM", RDBATHRM: "RDBATHRM",
	RDBNACRM: "RDBNACRM", RDBACCRM: "RDBACCRM", RDBAFLRM: "RDBAFLRM", ABNUOWRM: "ABNUOWRM",
	CMDATHRM: "CMDATHRM", CMDCHKRM: "CMDCHKRM", CMDNSPRM: "CMDNSPRM", AGNPRMRM: "AGNPRMRM",
	MGRLVLRM: "MGRLVLRM", OBJNSPRM: "OBJNSPRM", PRCCNVRM: "PRCCNVRM", PRMNSPRM: "PRMNSPRM",
	RSCLMTRM: "RSCLMTRM", SYNTAXRM: "SYNTAXRM", VALNSPRM: "VALNSPRM", DTAMCHRM: "DTAMCHRM",
	OPNQFLRM: "OPNQFLRM", RSLSETRM: "RSLSETRM",
	AGENT: "AGENT", SQLAM: "SQLAM", RDB: "RDB", SECMGR: "SECMGR", CMNTCPIP: "CMNTCPIP",
	UNICODEMGR: "UNICODEMGR", CCSIDMGR: "CCSIDMGR", MGRLVLLS: "MGRLVLLS", EXTNAM: "EXTNAM",
	SRVNAM: "SRVNAM", SRVRLSLV: "SRVRLSLV", SRVCLSNM: "SRVCLSNM", SECMEC: "SECMEC",
	SECTKN: "SECTKN", RDBNAM: "RDBNAM", USRID: "USRID", PASSWORD: "PASSWORD",
	PRDID: "PRDID", TYPDEFNAM: "TYPDEFNAM", TYPDEFOVR: "TYPDEFOVR", CRRTKN: "CRRTKN",
	RDBACCCL: "RDBACCCL", PKGNAMCSN: "PKGNAMCSN", QRYBLKSZ: "QRYBLKSZ", QRYINSID: "QRYINSID",
	SVRCOD: "SVRCOD", SECCHKCD: "SECCHKCD", SRVDGN: "SRVDGN", SYNERRCD: "SYNERRCD",
	CODPNT: "CODPNT", RTNSQLDA: "RTNSQLDA", TYPSQLDA: "TYPSQLDA", RDBCMTOK: "RDBCMTOK",
	MAXBLKEXT: "MAXBLKEXT", QRYCLSIMP: "QRYCLSIMP", RTNEXTDTA: "RTNEXTDTA", DYNDTAFMT: "DYNDTAFMT",
	UOWDSP: "UOWDSP", RSNCOD: "RSNCOD", RDBUPDRM: "RDBUPDRM",
}
