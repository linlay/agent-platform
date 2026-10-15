package mcp

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

// MCP tool mode reaches Agent-scoped MCP tools through mcp_search and
// mcp_call. A gateway tool ID is connectorId/mcpServer/toolName: readable,
// stable across Runs, and resolved again against the caller's mounts.
const (
	SearchDefaultLimit = 3
	SearchMaxLimit     = 5
)

// ComponentName recovers the mcp.json server name from its connector server
// key, which omits the default "main" component.
func ComponentName(connectorID, sourceKey string) string {
	connectorID, sourceKey = normalizeKey(connectorID), normalizeKey(sourceKey)
	if connectorID == "" {
		return ""
	}
	if sourceKey == "" || sourceKey == connectorID {
		return "main"
	}
	if rest, ok := strings.CutPrefix(sourceKey, connectorID+"."); ok && rest != "" {
		return rest
	}
	return ""
}

// GatewayToolID returns the gateway identity of a synchronized Agent-scoped
// MCP tool, or false for tools without connector provenance.
func GatewayToolID(def api.ToolDetailResponse) (string, bool) {
	connectorID := strings.TrimSpace(contracts.AnyStringNode(def.Meta["connectorId"]))
	component := strings.TrimSpace(contracts.AnyStringNode(def.Meta["mcpServer"]))
	name := strings.TrimSpace(contracts.AnyStringNode(def.Meta["mcpToolName"]))
	if connectorID == "" || component == "" || name == "" {
		return "", false
	}
	return connectorID + "/" + component + "/" + name, true
}

// ParseGatewayToolID splits connectorId/mcpServer/toolName. Connector IDs and
// server names cannot contain "/", so the remainder is the remote tool name.
func ParseGatewayToolID(id string) (connectorID, component, toolName string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(id), "/", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return "", "", "", false
		}
	}
	return parts[0], parts[1], parts[2], true
}

// MatchesGatewayToolID reports whether def is the tool named by id.
func MatchesGatewayToolID(def api.ToolDetailResponse, id string) bool {
	own, ok := GatewayToolID(def)
	if !ok {
		return false
	}
	return strings.EqualFold(own, strings.TrimSpace(id))
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

type searchDocument struct {
	def    api.ToolDetailResponse
	id     string
	terms  map[string]float64
	length float64
	exact  bool
}

// SearchTools ranks definitions for query with BM25 over the tool name, label,
// connector, description and parameters. An exact tool name, label or gateway
// ID match ranks first. Only definitions with a gateway ID are considered.
func SearchTools(defs []api.ToolDetailResponse, query string, limit int) []api.ToolDetailResponse {
	queryTerms := uniqueStrings(searchTokens(query))
	if len(queryTerms) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = SearchDefaultLimit
	}
	normalizedQuery := normalizeKey(query)
	docs := make([]searchDocument, 0, len(defs))
	documentFrequency := map[string]int{}
	totalLength := 0.0
	for _, def := range defs {
		id, ok := GatewayToolID(def)
		if !ok {
			continue
		}
		doc := searchDocument{def: def, id: id, terms: map[string]float64{}}
		name := contracts.AnyStringNode(def.Meta["mcpToolName"])
		doc.exact = normalizedQuery == normalizeKey(id) || normalizedQuery == normalizeKey(name) ||
			(strings.TrimSpace(def.Label) != "" && normalizedQuery == normalizeKey(def.Label))
		addSearchField(&doc, name, 3)
		addSearchField(&doc, def.Label, 2)
		addSearchField(&doc, contracts.AnyStringNode(def.Meta["connectorId"]), 1)
		addSearchField(&doc, def.Description, 1)
		addSchemaFields(&doc, def.Parameters)
		for term := range doc.terms {
			documentFrequency[term]++
		}
		totalLength += doc.length
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil
	}
	averageLength := totalLength / float64(len(docs))
	if averageLength <= 0 {
		averageLength = 1
	}
	type scored struct {
		doc   searchDocument
		score float64
	}
	hits := make([]scored, 0, len(docs))
	count := float64(len(docs))
	for _, doc := range docs {
		score := 0.0
		for _, term := range queryTerms {
			frequency := doc.terms[term]
			if frequency == 0 {
				continue
			}
			df := float64(documentFrequency[term])
			idf := math.Log(1 + (count-df+0.5)/(df+0.5))
			score += idf * frequency * (bm25K1 + 1) / (frequency + bm25K1*(1-bm25B+bm25B*doc.length/averageLength))
		}
		if score > 0 || doc.exact {
			hits = append(hits, scored{doc: doc, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].doc.exact != hits[j].doc.exact {
			return hits[i].doc.exact
		}
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].doc.id < hits[j].doc.id
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]api.ToolDetailResponse, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.doc.def)
	}
	return out
}

func addSearchField(doc *searchDocument, text string, weight float64) {
	for _, term := range searchTokens(text) {
		doc.terms[term] += weight
		doc.length += weight
	}
}

func addSchemaFields(doc *searchDocument, schema map[string]any) {
	properties, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		addSearchField(doc, name, 1)
		if property, ok := properties[name].(map[string]any); ok {
			addSearchField(doc, contracts.AnyStringNode(property["description"]), 1)
		}
	}
}

// searchTokens lowercases words, splits snake/kebab/camel case, and emits
// CJK runs as single characters plus bigrams so Chinese needs no dictionary.
func searchTokens(text string) []string {
	var tokens []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushCJK := func() {
		for i, r := range cjk {
			tokens = append(tokens, string(r))
			if i+1 < len(cjk) {
				tokens = append(tokens, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}
	var previous rune
	for _, r := range text {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			if len(word) > 0 && unicode.IsUpper(r) && unicode.IsLower(previous) {
				flushWord()
			}
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
		previous = r
	}
	flushWord()
	flushCJK()
	return tokens
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
