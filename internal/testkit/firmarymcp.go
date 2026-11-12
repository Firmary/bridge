package testkit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// FirmaryToken is a synthetic Firmary MCP access token (the contract's form: fmcp_ and 43 characters). Tests assert
// it never reaches a command line or a log.
const FirmaryToken = "fmcp_FIRMARY_TOKEN_CANARY_0123456789abcdefghijkl"

// FirmaryToolName is the one tool the fake Firmary MCP server offers, and FirmaryToolResult the text it returns.
const (
	FirmaryToolName   = "firmary_ping"
	FirmaryToolResult = "FIRMARY_TOOL_RESULT_6d"
)

// FirmaryMCP is a fake of Firmary's MCP server: Streamable HTTP at /api/mcp, answering JSON-RPC with plain JSON, one
// tool without annotations (so a provider's own approval rules would treat it as a write), and 401 for any request
// without "Authorization: Bearer <Token>".
type FirmaryMCP struct {
	URL   string // the endpoint, http://127.0.0.1:<port>/api/mcp
	Token string
	// InitDelay holds back the answer to initialize, as a slow network to Firmary would. Set it before the first
	// request.
	InitDelay time.Duration

	mu           sync.Mutex
	methods      []string
	unauthorized int
}

// NewFirmaryMCP starts a fake Firmary MCP server for token, closed when the test ends.
func NewFirmaryMCP(t *testing.T, token string) *FirmaryMCP {
	t.Helper()
	f := &FirmaryMCP{Token: token}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL + "/api/mcp"
	return f
}

// Methods are the JSON-RPC methods authorised requests called, in order.
func (f *FirmaryMCP) Methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// Unauthorized is how many requests came without the right bearer token.
func (f *FirmaryMCP) Unauthorized() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unauthorized
}

func (f *FirmaryMCP) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.Token {
		f.mu.Lock()
		f.unauthorized++
		f.mu.Unlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		// No server-initiated stream, and sessions end on their own.
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if json.Unmarshal(b, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.methods = append(f.methods, req.Method)
	f.mu.Unlock()
	if len(req.ID) == 0 || strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		time.Sleep(f.InitDelay)
		v := req.Params.ProtocolVersion
		if v == "" {
			v = "2025-06-18"
		}
		result = map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "firmary-fake", "version": "1.0.0"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": FirmaryToolName,
			"description": "Checks the connection to Firmary. Call it when asked to ping Firmary.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
	case "tools/call":
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": FirmaryToolResult}}}
	case "ping":
		result = map[string]any{}
	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "method not found"}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}
