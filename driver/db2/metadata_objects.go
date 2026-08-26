package db2

import (
	"context"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// getObjectsImpl implements adbc.Connection.GetObjects from the Db2
// system catalog (SYSCAT.SCHEMATA / TABLES / COLUMNS / TABCONST /
// KEYCOLUSE / REFERENCES). Db2 has a single catalog per connection —
// the database name — so the catalog level always has exactly one
// entry.
func (c *connectionImpl) getObjectsImpl(
	ctx context.Context,
	depth adbc.ObjectDepth,
	catalog, dbSchema, tableName, columnName *string,
	tableTypes []string,
) (array.RecordReader, error) {
	dbName := c.conn.Database()
	if catalog != nil && *catalog != "" && !strings.EqualFold(*catalog, dbName) {
		return buildGetObjectsRecordReader(c.alloc, nil)
	}
	entry := getObjectsInfo{CatalogName: &dbName, CatalogDbSchemas: []dbSchemaInfo{}}
	if depth != adbc.ObjectDepthCatalogs {
		schemas, err := c.listSchemas(ctx, dbSchema)
		if err != nil {
			return nil, err
		}
		includeColumns := depth == adbc.ObjectDepthAll || depth == adbc.ObjectDepthColumns
		for _, sch := range schemas {
			schName := sch
			se := dbSchemaInfo{DbSchemaName: &schName, DbSchemaTables: []tableInfo{}}
			if depth != adbc.ObjectDepthDBSchemas {
				tables, err := c.listTables(ctx, sch, tableName, columnName, tableTypes, includeColumns)
				if err != nil {
					return nil, err
				}
				se.DbSchemaTables = tables
			}
			entry.CatalogDbSchemas = append(entry.CatalogDbSchemas, se)
		}
	}
	return buildGetObjectsRecordReader(c.alloc, []getObjectsInfo{entry})
}

func (c *connectionImpl) listSchemas(ctx context.Context, filter *string) ([]string, error) {
	sb := strings.Builder{}
	sb.WriteString("SELECT SCHEMANAME FROM SYSCAT.SCHEMATA WHERE 1 = 1")
	appendLikeQual(&sb, "SCHEMANAME", filter)
	sb.WriteString(" ORDER BY SCHEMANAME")
	_, rows, err := c.queryAll(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, strings.TrimSpace(asString(r[0])))
	}
	return out, nil
}

func (c *connectionImpl) listTables(ctx context.Context, schema string, tableFilter, columnFilter *string, tableTypes []string, includeColumns bool) ([]tableInfo, error) {
	sb := strings.Builder{}
	sb.WriteString("SELECT T.TABNAME, T.TYPE, T.REMARKS FROM SYSCAT.TABLES T WHERE T.TABSCHEMA = ")
	sb.WriteString(sqlString(schema))
	appendLikeQual(&sb, "T.TABNAME", tableFilter)
	sb.WriteString(tableTypeFilter(tableTypes))
	sb.WriteString(" ORDER BY T.TABNAME")
	_, rows, err := c.queryAll(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	out := make([]tableInfo, 0, len(rows))
	var columnsByTable map[string][]columnInfo
	var constraintsByTable map[string][]constraintInfo
	if includeColumns && len(rows) > 0 {
		columnsByTable, err = c.listColumns(ctx, schema, tableFilter, columnFilter)
		if err != nil {
			return nil, err
		}
		constraintsByTable, err = c.listConstraints(ctx, schema, tableFilter)
		if err != nil {
			return nil, err
		}
	}
	for _, r := range rows {
		name := strings.TrimSpace(asString(r[0]))
		ti := tableInfo{
			TableName:        name,
			TableType:        db2TableType(asString(r[1])),
			TableColumns:     []columnInfo{},
			TableConstraints: []constraintInfo{},
		}
		if includeColumns {
			if cols, ok := columnsByTable[name]; ok {
				ti.TableColumns = cols
			}
			if cs, ok := constraintsByTable[name]; ok {
				ti.TableConstraints = cs
			}
		}
		out = append(out, ti)
	}
	return out, nil
}

// listColumns fetches every column of every matching table in one
// query and groups them by table.
func (c *connectionImpl) listColumns(ctx context.Context, schema string, tableFilter, columnFilter *string) (map[string][]columnInfo, error) {
	sb := strings.Builder{}
	sb.WriteString(`SELECT C.TABNAME, C.COLNAME, C.COLNO, C.TYPENAME, C.LENGTH, C.SCALE, C.NULLS, C.DEFAULT, C.REMARKS, C.IDENTITY, C.GENERATED, C.CODEPAGE
FROM SYSCAT.COLUMNS C WHERE C.TABSCHEMA = `)
	sb.WriteString(sqlString(schema))
	appendLikeQual(&sb, "C.TABNAME", tableFilter)
	appendLikeQual(&sb, "C.COLNAME", columnFilter)
	sb.WriteString(" ORDER BY C.TABNAME, C.COLNO")
	_, rows, err := c.queryAll(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	out := map[string][]columnInfo{}
	for _, r := range rows {
		tab := strings.TrimSpace(asString(r[0]))
		ci := columnInfo{ColumnName: strings.TrimSpace(asString(r[1]))}
		if n, ok := toInt64(r[2]); ok {
			ord := int32(n) + 1
			ci.OrdinalPosition = &ord
		}
		typeName := strings.TrimSpace(asString(r[3]))
		length, _ := toInt64(r[4])
		scale, _ := toInt64(r[5])
		ci.XdbcTypeName = strPtr(renderTypeName(typeName, length, scale))
		size := int32(length)
		ci.XdbcColumnSize = &size
		if scale != 0 || typeName == "DECIMAL" || typeName == "DECFLOAT" {
			dd := int16(scale)
			ci.XdbcDecimalDigits = &dd
		}
		radix := int16(10)
		ci.XdbcNumPrecRadix = &radix
		nullable := strings.TrimSpace(asString(r[6])) == "Y"
		nv := int16(0)
		yn := "NO"
		if nullable {
			nv, yn = 1, "YES"
		}
		ci.XdbcNullable = &nv
		ci.XdbcIsNullable = &yn
		if r[7] != nil {
			ci.XdbcColumnDef = strPtr(asString(r[7]))
		}
		if r[8] != nil {
			if rem := asString(r[8]); rem != "" {
				ci.Remarks = strPtr(rem)
			}
		}
		auto := strings.TrimSpace(asString(r[9])) == "Y"
		ci.XdbcIsAutoincrement = &auto
		gen := strings.TrimSpace(asString(r[10])) != ""
		ci.XdbcIsGeneratedcolumn = &gen
		dt := xdbcDataType(typeName)
		ci.XdbcDataType = &dt
		ci.XdbcSqlDataType = &dt
		out[tab] = append(out[tab], ci)
	}
	return out, nil
}

func renderTypeName(typeName string, length, scale int64) string {
	switch typeName {
	case "DECIMAL", "NUMERIC":
		return typeName + "(" + itoa(length) + "," + itoa(scale) + ")"
	case "CHARACTER", "CHAR", "VARCHAR", "GRAPHIC", "VARGRAPHIC", "BINARY", "VARBINARY", "CLOB", "BLOB", "DBCLOB", "NCHAR", "NVARCHAR", "NCLOB":
		return typeName + "(" + itoa(length) + ")"
	case "TIMESTAMP":
		return "TIMESTAMP(" + itoa(scale) + ")"
	}
	return typeName
}

// xdbcDataType maps a Db2 catalog type name to a JDBC java.sql.Types code.
func xdbcDataType(typeName string) int16 {
	switch typeName {
	case "SMALLINT":
		return 5
	case "INTEGER":
		return 4
	case "BIGINT":
		return -5
	case "DECIMAL", "NUMERIC":
		return 3
	case "DECFLOAT":
		return 1111
	case "REAL":
		return 7
	case "DOUBLE":
		return 8
	case "CHARACTER", "CHAR":
		return 1
	case "VARCHAR":
		return 12
	case "LONG VARCHAR":
		return -1
	case "GRAPHIC":
		return -15
	case "VARGRAPHIC":
		return -9
	case "CLOB":
		return 2005
	case "DBCLOB":
		return 2011
	case "BLOB":
		return 2004
	case "BINARY":
		return -2
	case "VARBINARY":
		return -3
	case "DATE":
		return 91
	case "TIME":
		return 92
	case "TIMESTAMP":
		return 93
	case "BOOLEAN":
		return 16
	case "XML":
		return 2009
	case "ROWID":
		return -8
	}
	return 1111
}

// listConstraints loads primary/unique/foreign keys for the schema.
func (c *connectionImpl) listConstraints(ctx context.Context, schema string, tableFilter *string) (map[string][]constraintInfo, error) {
	sb := strings.Builder{}
	sb.WriteString(`SELECT K.TABNAME, K.CONSTNAME, T.TYPE, K.COLNAME, K.COLSEQ
FROM SYSCAT.KEYCOLUSE K JOIN SYSCAT.TABCONST T ON T.TABSCHEMA = K.TABSCHEMA AND T.TABNAME = K.TABNAME AND T.CONSTNAME = K.CONSTNAME
WHERE K.TABSCHEMA = `)
	sb.WriteString(sqlString(schema))
	appendLikeQual(&sb, "K.TABNAME", tableFilter)
	sb.WriteString(" ORDER BY K.TABNAME, K.CONSTNAME, K.COLSEQ")
	_, rows, err := c.queryAll(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	type key struct{ tab, name string }
	order := []key{}
	byKey := map[key]*constraintInfo{}
	for _, r := range rows {
		k := key{strings.TrimSpace(asString(r[0])), strings.TrimSpace(asString(r[1]))}
		ci, ok := byKey[k]
		if !ok {
			name := k.name
			ct := "UNIQUE"
			switch strings.TrimSpace(asString(r[2])) {
			case "P":
				ct = "PRIMARY KEY"
			case "F":
				ct = "FOREIGN KEY"
			case "K":
				ct = "CHECK"
			}
			ci = &constraintInfo{ConstraintName: &name, ConstraintType: ct, ConstraintColumnNames: []string{}}
			byKey[k] = ci
			order = append(order, k)
		}
		ci.ConstraintColumnNames = append(ci.ConstraintColumnNames, strings.TrimSpace(asString(r[3])))
	}

	// Foreign-key targets.
	sb.Reset()
	sb.WriteString(`SELECT R.TABNAME, R.CONSTNAME, R.REFTABSCHEMA, R.REFTABNAME, K.COLNAME, K.COLSEQ
FROM SYSCAT.REFERENCES R JOIN SYSCAT.KEYCOLUSE K ON K.TABSCHEMA = R.REFTABSCHEMA AND K.TABNAME = R.REFTABNAME AND K.CONSTNAME = R.REFKEYNAME
WHERE R.TABSCHEMA = `)
	sb.WriteString(sqlString(schema))
	appendLikeQual(&sb, "R.TABNAME", tableFilter)
	sb.WriteString(" ORDER BY R.TABNAME, R.CONSTNAME, K.COLSEQ")
	_, rows, err = c.queryAll(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	dbName := c.conn.Database()
	for _, r := range rows {
		k := key{strings.TrimSpace(asString(r[0])), strings.TrimSpace(asString(r[1]))}
		ci, ok := byKey[k]
		if !ok {
			continue
		}
		refSchema := strings.TrimSpace(asString(r[2]))
		ci.ConstraintColumnUsage = append(ci.ConstraintColumnUsage, constraintColumnUsage{
			FkCatalog:    strPtr(dbName),
			FkDbSchema:   strPtr(refSchema),
			FkTable:      strings.TrimSpace(asString(r[3])),
			FkColumnName: strings.TrimSpace(asString(r[4])),
		})
	}
	out := map[string][]constraintInfo{}
	for _, k := range order {
		out[k.tab] = append(out[k.tab], *byKey[k])
	}
	return out, nil
}

func strPtr(s string) *string { return &s }

func appendLikeQual(sb *strings.Builder, col string, pattern *string) {
	if pattern == nil {
		return
	}
	sb.WriteString(" AND ")
	sb.WriteString(col)
	if *pattern == "" {
		sb.WriteString(" IS NULL")
		return
	}
	if strings.ContainsAny(*pattern, "%_") {
		sb.WriteString(" LIKE ")
		sb.WriteString(sqlString(*pattern))
		return
	}
	sb.WriteString(" = ")
	sb.WriteString(sqlString(*pattern))
}

var _ = drda.SQLTypeChar
