package online

import "encoding/json"

// MCPProfile is one catalogue a `quivr mcp` server can expose. A profile only
// narrows which tools an agent sees; what each call may reach is decided by
// the server from the API key, never by the profile.
type MCPProfile struct {
	Name    string
	Summary string
	// Instructions are sent to the agent when it connects: the workflow the
	// profile's tools are meant to follow.
	Instructions string
}

// MCPTool is one tool of the `quivr mcp` catalogue, declared as data so the
// server and a generated MCP reference read the same source.
type MCPTool struct {
	Name        string
	Title       string
	Description string
	// InputSchema is the JSON Schema of the tool arguments. It declares no
	// defaults, so an omitted argument keeps the server's own default.
	InputSchema json.RawMessage
	// Profiles lists the profiles that expose the tool.
	Profiles []string
	// ReadOnly promises the tool changes no data: it only reads through the
	// public API. Every tool of the read profile must be read-only.
	ReadOnly bool
}

// MCPProfiles lists the profiles in help order.
var MCPProfiles = []MCPProfile{
	{
		Name:    "read",
		Summary: "list reachable Corpora, search them and read Records; no tool changes data",
		Instructions: `Quivr stores documents as Records in Corpora and searches them with exact provenance.
Workflow: call list_corpora to learn which Corpora this connection may search, call search with a question and those corpus_ids, then call read_record on a hit when you need its full context.
To cite a hit, give its record_id, version_id, part_key and excerpt offsets [start,end). Offsets count Unicode code points in that Part's text, and the excerpt text is copied exactly from it.
Access is decided by the server from the API key: a Corpus you cannot reach is refused, never silently skipped.`,
	},
}

// MCPTools is the tool catalogue of every profile, in listing order.
var MCPTools = []MCPTool{
	{
		Name:  "list_corpora",
		Title: "List reachable Corpora",
		Description: `List the Corpora this connection's API key may read. Call it first to learn which corpus_ids you can pass to search.
Returns {"items": [{"corpus_id", "name", "effective_retrieval"}], "next_page_cursor"}. Only Corpora the key is authorized for appear. When next_page_cursor is present, call again with it as page_cursor to get the next page.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "page_cursor": {"type": "string", "minLength": 1, "description": "next_page_cursor from a previous list_corpora result; omit for the first page"}
  }
}`),
		Profiles: []string{"read"},
		ReadOnly: true,
	},
	{
		Name:  "search",
		Title: "Search Corpora",
		Description: `Search one or more Corpora for passages that answer a question. Use it to find evidence before answering or citing.
Returns {"items": [...], "retrieval_profile": {"name", "version"}}. Each item is a ranked hit: rank, record_id, version_id (the Record Version the passage comes from), part_key (the Part of that Version), and excerpt {text, start, end}, where text is the exact slice [start,end) of that Part's text, counted in Unicode code points. Cite a hit with record_id, version_id, part_key and the offsets. An empty items list means nothing matched; an unauthorized corpus_id is an error, never silently dropped.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["query", "corpus_ids"],
  "properties": {
    "query": {"type": "string", "minLength": 1, "maxLength": 8192, "description": "what to look for, in natural language or keywords"},
    "corpus_ids": {"type": "array", "minItems": 1, "maxItems": 16, "uniqueItems": true, "items": {"type": "string", "minLength": 1}, "description": "Corpora to search, from list_corpora"},
    "mode": {"type": "string", "enum": ["lexical", "semantic", "hybrid"], "description": "lexical matches words, semantic matches meaning, hybrid combines both; the server default is hybrid"},
    "profile": {"type": "string", "enum": ["fast", "balanced", "deep"], "description": "retrieval profile; the server default is balanced"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "maximum number of hits; the server default is 10"}
  }
}`),
		Profiles: []string{"read"},
		ReadOnly: true,
	},
	{
		Name:  "read_record",
		Title: "Read a Record",
		Description: `Read a Record and one of its Versions in full, to expand a search hit into its context or check a citation.
Pass the record_id of a hit, and its version_id to read exactly the Version the excerpt came from; omit version_id to read the Record's current Version.
Returns {"record": {"record_id", "source", "current_version_id", "withdrawn"}, "version": {"version_id", "record_id", "availability", "manifest", "processing", "relations", ...}}. version.manifest.parts holds every Part with its key, role and content; a hit's excerpt offsets index the text of the Part whose key is the hit's part_key. When record.current_version_id differs from the Version you cite, a newer Version of the Record exists.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["record_id"],
  "properties": {
    "record_id": {"type": "string", "minLength": 1, "description": "record_id of a search hit"},
    "version_id": {"type": "string", "minLength": 1, "description": "version_id to read; omit for the Record's current Version"}
  }
}`),
		Profiles: []string{"read"},
		ReadOnly: true,
	},
}

// LookupMCPProfile returns the profile called name.
func LookupMCPProfile(name string) (MCPProfile, bool) {
	for _, p := range MCPProfiles {
		if p.Name == name {
			return p, true
		}
	}
	return MCPProfile{}, false
}

// MCPToolsFor returns the tools the profile called name exposes, in listing order.
func MCPToolsFor(name string) []MCPTool {
	var tools []MCPTool
	for _, t := range MCPTools {
		for _, p := range t.Profiles {
			if p == name {
				tools = append(tools, t)
				break
			}
		}
	}
	return tools
}
