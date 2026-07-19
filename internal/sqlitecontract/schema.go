package sqlitecontract

import (
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// SchemaBuilder creates the current store schema on an empty SQLite database.
// It is the sole source for canonical structural comparison, so validation
// cannot drift from the DDL that creates a new runtime store.
type SchemaBuilder func(*sql.DB) error

type schemaSnapshot struct {
	Tables   []tableShape
	Indexes  []indexShape
	Triggers []triggerShape
}

type tableShape struct {
	Name         string
	VirtualSQL   string
	WithoutRowID bool
	Strict       bool
	Columns      []columnShape
	ForeignKeys  []foreignKeyShape
	Checks       []string
	Constraints  []string
}

type columnShape struct {
	Name       string
	Type       string
	NotNull    bool
	DefaultSQL string
	PrimaryKey int
	Hidden     int
	Definition string
}

type foreignKeyShape struct {
	ID       int
	Sequence int
	Table    string
	From     string
	To       string
	OnUpdate string
	OnDelete string
	Match    string
}

type indexShape struct {
	Name    string
	Table   string
	Unique  bool
	Origin  string
	Partial bool
	SQL     string
	Columns []indexColumnShape
}

type indexColumnShape struct {
	Sequence  int
	Name      string
	Desc      bool
	Collation string
	Key       bool
}

type triggerShape struct {
	Name  string
	Table string
	SQL   string
}

type canonicalResult struct {
	snapshot schemaSnapshot
	err      error
}

var canonicalSchemas sync.Map // map[string]canonicalResult

// VerifyStructure checks a database against the complete semantic schema of
// the current store. Column physical order is intentionally ignored; primary
// key and index key ordering remain part of the comparison.
func VerifyStructure(db *sql.DB, databasePath, cleanupPath string, spec Spec) error {
	if spec.BuildCanonical == nil {
		return unsupported(databasePath, cleanupPath, "schema contract does not define a canonical builder")
	}
	expected, err := canonicalSchema(spec)
	if err != nil {
		return fmt.Errorf("build canonical SQLite schema %q: %w", spec.Name, err)
	}
	actual, err := inspectSchema(db)
	if err != nil {
		return fmt.Errorf("inspect SQLite schema: %w", err)
	}
	if reason := compareSchema(expected, actual); reason != "" {
		return unsupported(databasePath, cleanupPath, "schema mismatch: "+reason)
	}
	return verifyRequiredObjects(db, databasePath, cleanupPath, spec)
}

func canonicalSchema(spec Spec) (schemaSnapshot, error) {
	key := strings.TrimSpace(spec.Name)
	if key != "" {
		if cached, ok := canonicalSchemas.Load(key); ok {
			result := cached.(canonicalResult)
			return result.snapshot, result.err
		}
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return schemaSnapshot{}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := spec.BuildCanonical(db); err != nil {
		return schemaSnapshot{}, err
	}
	snapshot, err := inspectSchema(db)
	if key != "" {
		actual, _ := canonicalSchemas.LoadOrStore(key, canonicalResult{snapshot: snapshot, err: err})
		result := actual.(canonicalResult)
		return result.snapshot, result.err
	}
	return snapshot, err
}

func inspectSchema(db *sql.DB) (schemaSnapshot, error) {
	tableFlags, err := inspectTableFlags(db)
	if err != nil {
		return schemaSnapshot{}, err
	}
	rows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table', 'index', 'trigger')
		  AND (type = 'index' OR name NOT LIKE 'sqlite_%')
		ORDER BY type, name`)
	if err != nil {
		return schemaSnapshot{}, err
	}
	type masterObject struct {
		kind    string
		name    string
		table   string
		sqlText string
	}
	objects := make([]masterObject, 0)
	for rows.Next() {
		var item masterObject
		if err := rows.Scan(&item.kind, &item.name, &item.table, &item.sqlText); err != nil {
			_ = rows.Close()
			return schemaSnapshot{}, err
		}
		objects = append(objects, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return schemaSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return schemaSnapshot{}, err
	}
	var snapshot schemaSnapshot
	for _, item := range objects {
		switch strings.ToLower(item.kind) {
		case "table":
			shape, err := inspectTable(db, item.name, item.sqlText, tableFlags[strings.ToLower(item.name)])
			if err != nil {
				return schemaSnapshot{}, err
			}
			snapshot.Tables = append(snapshot.Tables, shape)
		case "index":
			shape, err := inspectIndex(db, item.name, item.table, item.sqlText)
			if err != nil {
				return schemaSnapshot{}, err
			}
			snapshot.Indexes = append(snapshot.Indexes, shape)
		case "trigger":
			snapshot.Triggers = append(snapshot.Triggers, triggerShape{Name: item.name, Table: item.table, SQL: normalizeSQL(item.sqlText)})
		default:
			return schemaSnapshot{}, fmt.Errorf("unsupported sqlite schema object type %q for %s", item.kind, item.name)
		}
	}
	sort.Slice(snapshot.Tables, func(i, j int) bool { return snapshot.Tables[i].Name < snapshot.Tables[j].Name })
	sort.Slice(snapshot.Indexes, func(i, j int) bool { return snapshot.Indexes[i].Name < snapshot.Indexes[j].Name })
	sort.Slice(snapshot.Triggers, func(i, j int) bool { return snapshot.Triggers[i].Name < snapshot.Triggers[j].Name })
	return snapshot, nil
}

func inspectTableFlags(db *sql.DB) (map[string]tableFlags, error) {
	rows, err := db.Query("PRAGMA table_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]tableFlags{}
	for rows.Next() {
		var schema, name, kind string
		var columns, withoutRowID, strict int
		if err := rows.Scan(&schema, &name, &kind, &columns, &withoutRowID, &strict); err != nil {
			return nil, err
		}
		if schema == "main" && kind == "table" {
			result[strings.ToLower(name)] = tableFlags{withoutRowID: withoutRowID != 0, strict: strict != 0}
		}
	}
	return result, rows.Err()
}

type tableFlags struct {
	withoutRowID bool
	strict       bool
}

func inspectTable(db *sql.DB, name, sqlText string, flags tableFlags) (tableShape, error) {
	shape := tableShape{Name: name, WithoutRowID: flags.withoutRowID, Strict: flags.strict}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlText)), "CREATE VIRTUAL TABLE") {
		shape.VirtualSQL = normalizeSQL(sqlText)
	}
	columnDefinitions, tableConstraints := tableDefinitions(sqlText)
	rows, err := db.Query("PRAGMA table_xinfo(" + quoteIdentifier(name) + ")")
	if err != nil {
		return tableShape{}, err
	}
	for rows.Next() {
		var cid, notNull, primaryKey, hidden int
		var columnName, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			_ = rows.Close()
			return tableShape{}, err
		}
		shape.Columns = append(shape.Columns, columnShape{
			Name: columnName, Type: normalizeType(columnType), NotNull: notNull != 0,
			DefaultSQL: normalizeDefault(defaultValue), PrimaryKey: primaryKey, Hidden: hidden,
			Definition: columnDefinitions[strings.ToLower(columnName)],
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return tableShape{}, err
	}
	_ = rows.Close()
	sort.Slice(shape.Columns, func(i, j int) bool {
		return strings.ToLower(shape.Columns[i].Name) < strings.ToLower(shape.Columns[j].Name)
	})

	foreignKeys, err := db.Query("PRAGMA foreign_key_list(" + quoteIdentifier(name) + ")")
	if err != nil {
		return tableShape{}, err
	}
	for foreignKeys.Next() {
		var item foreignKeyShape
		if err := foreignKeys.Scan(&item.ID, &item.Sequence, &item.Table, &item.From, &item.To, &item.OnUpdate, &item.OnDelete, &item.Match); err != nil {
			_ = foreignKeys.Close()
			return tableShape{}, err
		}
		shape.ForeignKeys = append(shape.ForeignKeys, item)
	}
	if err := foreignKeys.Err(); err != nil {
		_ = foreignKeys.Close()
		return tableShape{}, err
	}
	_ = foreignKeys.Close()
	sort.Slice(shape.ForeignKeys, func(i, j int) bool {
		left, right := shape.ForeignKeys[i], shape.ForeignKeys[j]
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		return left.Sequence < right.Sequence
	})
	shape.Checks = extractChecks(sqlText)
	shape.Constraints = tableConstraints
	return shape, nil
}

func inspectIndex(db *sql.DB, name, table, sqlText string) (indexShape, error) {
	shape := indexShape{Name: name, Table: table, SQL: normalizeSQL(sqlText)}
	rows, err := db.Query("PRAGMA index_list(" + quoteIdentifier(table) + ")")
	if err != nil {
		return indexShape{}, err
	}
	found := false
	for rows.Next() {
		var sequence, unique, partial int
		var indexName, origin string
		if err := rows.Scan(&sequence, &indexName, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return indexShape{}, err
		}
		if indexName == name {
			shape.Unique, shape.Origin, shape.Partial = unique != 0, origin, partial != 0
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return indexShape{}, err
	}
	_ = rows.Close()
	if !found {
		return indexShape{}, fmt.Errorf("index %s missing from PRAGMA index_list(%s)", name, table)
	}
	columns, err := db.Query("PRAGMA index_xinfo(" + quoteIdentifier(name) + ")")
	if err != nil {
		return indexShape{}, err
	}
	for columns.Next() {
		var item indexColumnShape
		var desc, key int
		var name sql.NullString
		if err := columns.Scan(&item.Sequence, new(int), &name, &desc, &item.Collation, &key); err != nil {
			_ = columns.Close()
			return indexShape{}, err
		}
		item.Name = name.String
		item.Desc, item.Key = desc != 0, key != 0
		shape.Columns = append(shape.Columns, item)
	}
	if err := columns.Err(); err != nil {
		_ = columns.Close()
		return indexShape{}, err
	}
	_ = columns.Close()
	sort.Slice(shape.Columns, func(i, j int) bool { return shape.Columns[i].Sequence < shape.Columns[j].Sequence })
	return shape, nil
}

func compareSchema(expected, actual schemaSnapshot) string {
	if reason := compareTables(expected.Tables, actual.Tables); reason != "" {
		return reason
	}
	if reason := compareIndexes(expected.Indexes, actual.Indexes); reason != "" {
		return reason
	}
	return compareTriggers(expected.Triggers, actual.Triggers)
}

func compareTables(expected, actual []tableShape) string {
	if !reflect.DeepEqual(namesOfTables(expected), namesOfTables(actual)) {
		return fmt.Sprintf("table set differs (expected=%v actual=%v)", namesOfTables(expected), namesOfTables(actual))
	}
	for index, want := range expected {
		got := actual[index]
		if want.VirtualSQL != got.VirtualSQL || want.WithoutRowID != got.WithoutRowID || want.Strict != got.Strict {
			return fmt.Sprintf("table %s definition differs (expected virtual=%q withoutRowID=%t strict=%t; actual virtual=%q withoutRowID=%t strict=%t)", want.Name, want.VirtualSQL, want.WithoutRowID, want.Strict, got.VirtualSQL, got.WithoutRowID, got.Strict)
		}
		if !reflect.DeepEqual(want.Columns, got.Columns) {
			return fmt.Sprintf("table %s columns differ (expected=%v actual=%v)", want.Name, want.Columns, got.Columns)
		}
		if !reflect.DeepEqual(want.ForeignKeys, got.ForeignKeys) {
			return fmt.Sprintf("table %s foreign keys differ (expected=%v actual=%v)", want.Name, want.ForeignKeys, got.ForeignKeys)
		}
		if !reflect.DeepEqual(want.Checks, got.Checks) {
			return fmt.Sprintf("table %s CHECK constraints differ (expected=%v actual=%v)", want.Name, want.Checks, got.Checks)
		}
		if !reflect.DeepEqual(want.Constraints, got.Constraints) {
			return fmt.Sprintf("table %s constraints differ (expected=%v actual=%v)", want.Name, want.Constraints, got.Constraints)
		}
	}
	return ""
}

// tableDefinitions retains the parts of CREATE TABLE that SQLite's PRAGMAs
// do not expose (for example COLLATE, generated expressions, conflict
// handling and deferred foreign keys). Terms are keyed/sorted rather than
// compared in source order, so a physical column reorder remains compatible.
func tableDefinitions(sqlText string) (map[string]string, []string) {
	definitions := map[string]string{}
	start := strings.Index(sqlText, "(")
	if start < 0 {
		return definitions, nil
	}
	end := matchingParenthesis(sqlText, start)
	if end <= start {
		return definitions, nil
	}
	var constraints []string
	for _, term := range splitTopLevelComma(sqlText[start+1 : end]) {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		upper := strings.ToUpper(term)
		if startsTableConstraint(upper) {
			constraints = append(constraints, normalizeSQL(term))
			continue
		}
		name, remainder, ok := leadingIdentifier(term)
		if !ok {
			constraints = append(constraints, normalizeSQL(term))
			continue
		}
		definitions[strings.ToLower(name)] = normalizeSQL(remainder)
	}
	sort.Strings(constraints)
	return definitions, constraints
}

func startsTableConstraint(upper string) bool {
	for _, prefix := range []string{"CONSTRAINT ", "PRIMARY ", "UNIQUE ", "FOREIGN ", "CHECK "} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func leadingIdentifier(value string) (name, remainder string, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	switch value[0] {
	case '`', '"':
		quote := value[0]
		for index := 1; index < len(value); index++ {
			if value[index] == quote {
				return value[1:index], strings.TrimSpace(value[index+1:]), true
			}
		}
		return "", "", false
	case '[':
		if index := strings.IndexByte(value, ']'); index > 0 {
			return value[1:index], strings.TrimSpace(value[index+1:]), true
		}
		return "", "", false
	}
	for index := 0; index < len(value); index++ {
		if value[index] == ' ' || value[index] == '\t' || value[index] == '\n' || value[index] == '\r' {
			return value[:index], strings.TrimSpace(value[index:]), true
		}
	}
	return value, "", true
}

func matchingParenthesis(value string, start int) int {
	depth := 0
	var quote byte
	for index := start; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			if current == quote {
				if index+1 < len(value) && value[index+1] == quote && (quote == '\'' || quote == '"') {
					index++
					continue
				}
				quote = 0
			}
			continue
		}
		switch current {
		case '\'', '"', '`':
			quote = current
		case '[':
			quote = ']'
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func splitTopLevelComma(value string) []string {
	var parts []string
	start, depth := 0, 0
	var quote byte
	for index := 0; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			if current == quote {
				if index+1 < len(value) && value[index+1] == quote && (quote == '\'' || quote == '"') {
					index++
					continue
				}
				quote = 0
			}
			continue
		}
		switch current {
		case '\'', '"', '`':
			quote = current
		case '[':
			quote = ']'
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, value[start:index])
				start = index + 1
			}
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func compareIndexes(expected, actual []indexShape) string {
	if !reflect.DeepEqual(namesOfIndexes(expected), namesOfIndexes(actual)) {
		return fmt.Sprintf("index set differs (expected=%v actual=%v)", namesOfIndexes(expected), namesOfIndexes(actual))
	}
	for index, want := range expected {
		got := actual[index]
		if want.Name != got.Name || want.Table != got.Table || want.Unique != got.Unique || want.Origin != got.Origin || want.Partial != got.Partial || want.SQL != got.SQL {
			return fmt.Sprintf("index %s definition differs (expected=%+v actual=%+v)", want.Name, want, got)
		}
		if !reflect.DeepEqual(want.Columns, got.Columns) {
			return fmt.Sprintf("index %s keys differ (expected=%v actual=%v)", want.Name, want.Columns, got.Columns)
		}
	}
	return ""
}

func compareTriggers(expected, actual []triggerShape) string {
	if !reflect.DeepEqual(namesOfTriggers(expected), namesOfTriggers(actual)) {
		return fmt.Sprintf("trigger set differs (expected=%v actual=%v)", namesOfTriggers(expected), namesOfTriggers(actual))
	}
	for index, want := range expected {
		got := actual[index]
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("trigger %s differs (expected table=%s sql=%q; actual table=%s sql=%q)", want.Name, want.Table, want.SQL, got.Table, got.SQL)
		}
	}
	return ""
}

func namesOfTables(items []tableShape) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}
func namesOfIndexes(items []indexShape) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}
func namesOfTriggers(items []triggerShape) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func normalizeSQL(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
func normalizeType(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}
func normalizeDefault(value any) string {
	if value == nil {
		return ""
	}
	return normalizeSQL(fmt.Sprint(value))
}

func extractChecks(sqlText string) []string {
	upper := strings.ToUpper(sqlText)
	var checks []string
	for offset := 0; ; {
		index := strings.Index(upper[offset:], "CHECK")
		if index < 0 {
			break
		}
		start := offset + index + len("CHECK")
		for start < len(sqlText) && (sqlText[start] == ' ' || sqlText[start] == '\t' || sqlText[start] == '\n' || sqlText[start] == '\r') {
			start++
		}
		if start >= len(sqlText) || sqlText[start] != '(' {
			offset = start
			continue
		}
		depth, end := 0, start
		for ; end < len(sqlText); end++ {
			switch sqlText[end] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end++
					goto found
				}
			}
		}
	found:
		if end > start {
			checks = append(checks, normalizeSQL(sqlText[start:end]))
		}
		offset = end
		if offset >= len(sqlText) {
			break
		}
	}
	sort.Strings(checks)
	return checks
}
