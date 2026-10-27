package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanehull/obsidian-remote/internal/config"
	"github.com/shanehull/obsidian-remote/internal/obsidian"
)

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want []string
	}{
		{"empty", nil, nil},
		{"no_hash", []string{"tag1", "tag2"}, []string{"tag1", "tag2"}},
		{"with_hash", []string{"#tag1", "#tag2"}, []string{"tag1", "tag2"}},
		{"mixed", []string{"#tag1", "tag2", "#tag3"}, []string{"tag1", "tag2", "tag3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTags(tt.tags)
			if tt.tags == nil && got == nil {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("normalizeTags(%v) = %v, want %v", tt.tags, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("normalizeTags(%v) = %v, want %v", tt.tags, got, tt.want)
				}
			}
		})
	}
}

func TestAddTagToSlice(t *testing.T) {
	tests := []struct {
		name   string
		tags   []string
		tag    string
		wantOK bool
	}{
		{"new_tag", []string{"a", "b"}, "c", true},
		{"existing_tag", []string{"a", "b"}, "a", false},
		{"empty_slice", nil, "a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := addTagToSlice(tt.tags, tt.tag)
			if ok != tt.wantOK {
				t.Fatalf("addTagToSlice(%v, %q) ok = %v, want %v", tt.tags, tt.tag, ok, tt.wantOK)
			}
			if ok {
				found := false
				for _, r := range result {
					if r == tt.tag {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("addTagToSlice(%v, %q) = %v, missing new tag", tt.tags, tt.tag, result)
				}
				if len(result) != len(tt.tags)+1 {
					t.Fatalf("addTagToSlice(%v, %q) len = %d, want %d", tt.tags, tt.tag, len(result), len(tt.tags)+1)
				}
			}
		})
	}
}

func TestRemoveTagFromSlice(t *testing.T) {
	tests := []struct {
		name   string
		tags   []string
		tag    string
		wantOK bool
	}{
		{"existing_tag", []string{"a", "b"}, "a", true},
		{"missing_tag", []string{"a", "b"}, "c", false},
		{"last_tag", []string{"a"}, "a", true},
		{"multiple_occurrences", []string{"a", "b", "a"}, "a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := removeTagFromSlice(tt.tags, tt.tag)
			if ok != tt.wantOK {
				t.Fatalf("removeTagFromSlice(%v, %q) ok = %v, want %v", tt.tags, tt.tag, ok, tt.wantOK)
			}
			if ok {
				for _, r := range result {
					if r == tt.tag {
						t.Fatalf("removeTagFromSlice(%v, %q) = %v, tag still present", tt.tags, tt.tag, result)
					}
				}
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"no_change", "notes/mynote.md", "notes/mynote.md"},
		{"leading_slash", "/notes/mynote.md", "notes/mynote.md"},
		{"trailing_slash", "notes/", "notes"},
		{"both_slashes", "/notes/mynote.md/", "notes/mynote.md"},
		{"empty", "", ""},
		{"root_path", "/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePath(tt.path); got != tt.want {
				t.Fatalf("normalizePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *obsidian.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return obsidian.NewClient(&config.Config{
		ObsidianURL: srv.URL,
		ObsidianKey: "test-key",
	})
}

func mustMarshalArgs(args map[string]any) json.RawMessage {
	data, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return data
}

func getText(result *mcp.CallToolResult) string {
	return result.Content[0].(*mcp.TextContent).Text
}

func TestHandleReadNote(t *testing.T) {
	t.Run("full note", func(t *testing.T) {
		var gotMethod, gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# Hello"))
		})

		handler := handleReadNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path": "notes/test.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if gotMethod != "GET" {
			t.Fatalf("method = %q, want GET", gotMethod)
		}
		if gotPath != "/vault/notes/test.md" {
			t.Fatalf("path = %q, want /vault/notes/test.md", gotPath)
		}
	})

	t.Run("heading target via URL path", func(t *testing.T) {
		var gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		})

		handler := handleReadNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":        "notes/test.md",
			"target_type": "heading",
			"target":      "My Section",
		})}}
		_, _ = handler(context.Background(), req)
		if gotPath != "/vault/notes/test.md/heading/My Section" {
			t.Fatalf("path = %q, want /vault/notes/test.md/heading/My Section", gotPath)
		}
	})

	t.Run("frontmatter target via URL path", func(t *testing.T) {
		var gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		})

		handler := handleReadNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":        "notes/test.md",
			"target_type": "frontmatter",
			"target":      "tags",
		})}}
		_, _ = handler(context.Background(), req)
		if gotPath != "/vault/notes/test.md/frontmatter/tags" {
			t.Fatalf("path = %q, want /vault/notes/test.md/frontmatter/tags", gotPath)
		}
	})

	t.Run("nested heading via URL path", func(t *testing.T) {
		var gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		})

		handler := handleReadNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":             "notes/test.md",
			"target_type":      "heading",
			"target":           "Work::Meetings",
			"target_delimiter": "::",
		})}}
		_, _ = handler(context.Background(), req)
		if gotPath != "/vault/notes/test.md/heading/Work/Meetings" {
			t.Fatalf("path = %q, want /vault/notes/test.md/heading/Work/Meetings", gotPath)
		}
	})

	t.Run("missing path", func(t *testing.T) {
		handler := handleReadNote(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing path")
		}
	})
}

func TestHandleMoveNote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotMethod, gotPath, gotDest string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotDest = r.Header.Get("Destination")
			w.WriteHeader(http.StatusNoContent)
		})

		handler := handleMoveNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":    "notes/old.md",
			"newPath": "notes/new.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if gotMethod != "MOVE" {
			t.Fatalf("method = %q, want MOVE", gotMethod)
		}
		if gotPath != "/vault/notes/old.md" {
			t.Fatalf("path = %q, want /vault/notes/old.md", gotPath)
		}
		if gotDest != "notes/new.md" {
			t.Fatalf("Destination = %q, want notes/new.md", gotDest)
		}
		if !strings.Contains(getText(result), "Successfully moved") {
			t.Fatalf("unexpected result text: %s", getText(result))
		}
	})

	t.Run("success with allowOverwrite", func(t *testing.T) {
		var gotOverwrite string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotOverwrite = r.Header.Get("Allow-Overwrite")
			w.WriteHeader(http.StatusNoContent)
		})

		handler := handleMoveNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":           "notes/old.md",
			"newPath":        "notes/exists.md",
			"allowOverwrite": "true",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if gotOverwrite != "true" {
			t.Fatalf("Allow-Overwrite = %q, want true", gotOverwrite)
		}
	})

	t.Run("missing path", func(t *testing.T) {
		handler := handleMoveNote(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"newPath": "notes/new.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing path")
		}
	})

	t.Run("missing newPath", func(t *testing.T) {
		handler := handleMoveNote(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path": "notes/old.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing newPath")
		}
	})

	t.Run("api error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`not found`))
		})

		handler := handleMoveNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":    "notes/missing.md",
			"newPath": "notes/target.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error wrapping api error")
		}
	})
}

func TestHandleListNotes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotMethod, gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`["note.md"]`))
		})

		handler := handleListNotes(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if gotMethod != "GET" {
			t.Fatalf("method = %q, want GET", gotMethod)
		}
		if gotPath != "/vault/" {
			t.Fatalf("path = %q, want /vault/", gotPath)
		}
	})

	t.Run("with subdir", func(t *testing.T) {
		var gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`["note.md"]`))
		})

		handler := handleListNotes(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"dirPath": "notes/sub",
		})}}
		_, _ = handler(context.Background(), req)
		if gotPath != "/vault/notes/sub" {
			t.Fatalf("path = %q, want /vault/notes/sub", gotPath)
		}
	})

	t.Run("api error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		handler := handleListNotes(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error wrapping api error")
		}
	})
}

func TestHandleDeleteNote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotMethod, gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		})

		handler := handleDeleteNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path": "notes/test.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if gotMethod != "DELETE" {
			t.Fatalf("method = %q, want DELETE", gotMethod)
		}
		if gotPath != "/vault/notes/test.md" {
			t.Fatalf("path = %q, want /vault/notes/test.md", gotPath)
		}
	})

	t.Run("missing path", func(t *testing.T) {
		handler := handleDeleteNote(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing path")
		}
	})

	t.Run("api error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		handler := handleDeleteNote(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path": "notes/missing.md",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error wrapping api error")
		}
	})
}

func TestHandleGlobalSearch(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotMethod, gotPath string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.RequestURI()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		})

		handler := handleGlobalSearch(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"query": "test",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if gotMethod != "POST" {
			t.Fatalf("method = %q, want POST", gotMethod)
		}
		if gotPath != "/search/simple/?query=test" {
			t.Fatalf("path = %q, want /search/simple/?query=test", gotPath)
		}
	})

	t.Run("missing query", func(t *testing.T) {
		handler := handleGlobalSearch(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing query")
		}
	})
}

func TestHandleSearchReplace(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var getCalled, putCalled bool
		var putBody string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				getCalled = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("hello world"))
			default:
				putCalled = true
				buf := make([]byte, 1024)
				n, _ := r.Body.Read(buf)
				putBody = string(buf[:n])
				w.WriteHeader(http.StatusNoContent)
			}
		})

		handler := handleSearchReplace(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":    "notes/test.md",
			"search":  "hello",
			"replace": "hi",
			"count":   float64(-1),
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		if !getCalled || !putCalled {
			t.Fatal("expected both GET and PUT calls")
		}
		if putBody != "hi world" {
			t.Fatalf("putBody = %q, want %q", putBody, "hi world")
		}
	})

	t.Run("search text not found", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hello world"))
		})

		handler := handleSearchReplace(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":    "notes/test.md",
			"search":  "xyz",
			"replace": "hi",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error when search text not found")
		}
	})

	t.Run("missing params", func(t *testing.T) {
		handler := handleSearchReplace(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing params")
		}
	})
}

func TestHandleManageTags(t *testing.T) {
	t.Run("add tag success", func(t *testing.T) {
		var patchBody string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"tags":["existing"]}`))
			default:
				buf := make([]byte, 1024)
				n, _ := r.Body.Read(buf)
				patchBody = string(buf[:n])
				w.WriteHeader(http.StatusNoContent)
			}
		})

		handler := handleManageTags(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":      "notes/test.md",
			"operation": "add",
			"tag":       "newtag",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(patchBody), &parsed) != nil {
			t.Fatalf("patch body not valid JSON: %s", patchBody)
		}
		if parsed["targetType"] != "frontmatter" {
			t.Fatalf("targetType = %v, want frontmatter", parsed["targetType"])
		}
		if parsed["operation"] != "replace" {
			t.Fatalf("operation = %v, want replace", parsed["operation"])
		}
	})

	t.Run("remove tag success", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"tags":["a","b"]}`))
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		})

		handler := handleManageTags(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":      "notes/test.md",
			"operation": "remove",
			"tag":       "a",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
	})

	t.Run("invalid operation", func(t *testing.T) {
		handler := handleManageTags(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":      "notes/test.md",
			"operation": "invalid",
			"tag":       "x",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for invalid operation")
		}
	})

	t.Run("missing params", func(t *testing.T) {
		handler := handleManageTags(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing params")
		}
	})
}

func TestHandleManageFrontmatter(t *testing.T) {
	t.Run("get success", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"title":"test"}`))
		})

		handler := handleManageFrontmatter(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":      "notes/test.md",
			"operation": "get",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
	})

	t.Run("set success", func(t *testing.T) {
		var patchBody string
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			patchBody = string(buf[:n])
			w.WriteHeader(http.StatusNoContent)
		})

		handler := handleManageFrontmatter(client)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":        "notes/test.md",
			"operation":   "set",
			"jsonPayload": `{"title":"hello"}`,
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error: %v", getText(result))
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(patchBody), &parsed) != nil {
			t.Fatalf("patch body not valid JSON: %s", patchBody)
		}
		if parsed["targetType"] != "frontmatter" {
			t.Fatalf("targetType = %v, want frontmatter", parsed["targetType"])
		}
		if parsed["target"] != "title" {
			t.Fatalf("target = %v, want title", parsed["target"])
		}
	})

	t.Run("set with invalid json", func(t *testing.T) {
		handler := handleManageFrontmatter(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":        "notes/test.md",
			"operation":   "set",
			"jsonPayload": `not json`,
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for invalid json")
		}
	})

	t.Run("invalid operation", func(t *testing.T) {
		handler := handleManageFrontmatter(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
			"path":      "notes/test.md",
			"operation": "delete",
		})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for invalid operation")
		}
	})

	t.Run("missing params", func(t *testing.T) {
		handler := handleManageFrontmatter(nil)
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{})}}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected error for missing params")
		}
	})
}

// legacyPatchRecorder emulates a Local REST API old enough to require
// header-based targeting: it rejects the JSON-body PATCH the way the plugin
// does, then records the retry.
type legacyPatchRecorder struct {
	calls   int
	headers http.Header
	body    string
}

func (r *legacyPatchRecorder) handler(w http.ResponseWriter, req *http.Request) {
	r.calls++
	if req.Header.Get("Target-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorCode":40053,"message":"No 'Target-Type' header was provided."}`))
		return
	}
	body, _ := io.ReadAll(req.Body)
	r.headers = req.Header.Clone()
	r.body = string(body)
	w.WriteHeader(http.StatusNoContent)
}

func TestHandleManageFrontmatterLegacyFallback(t *testing.T) {
	rec := &legacyPatchRecorder{}
	client := newTestClient(t, rec.handler)

	handler := handleManageFrontmatter(client)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
		"path":        "notes/test.md",
		"operation":   "set",
		"jsonPayload": `{"status":"encerrado"}`,
	})}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success after fallback, got error: %v", getText(result))
	}
	if rec.calls != 2 {
		t.Fatalf("calls = %d, want 2 (JSON attempt + legacy retry)", rec.calls)
	}
	if got := rec.headers.Get("Target-Type"); got != "frontmatter" {
		t.Fatalf("Target-Type = %q, want frontmatter", got)
	}
	if got := rec.headers.Get("Target"); got != "status" {
		t.Fatalf("Target = %q, want status", got)
	}
	if got := rec.headers.Get("Operation"); got != "replace" {
		t.Fatalf("Operation = %q, want replace", got)
	}
	if rec.body != `"encerrado"` {
		t.Fatalf("legacy body = %s, want the bare JSON value", rec.body)
	}
}

func TestHandleUpdateNoteLegacyFallback(t *testing.T) {
	rec := &legacyPatchRecorder{}
	client := newTestClient(t, rec.handler)

	handler := handleUpdateNote(client)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
		"path":        "notes/test.md",
		"content":     "novo texto",
		"operation":   "append",
		"target_type": "heading",
		"target":      "Topo::Referências",
	})}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success after fallback, got error: %v", getText(result))
	}
	if rec.calls != 2 {
		t.Fatalf("calls = %d, want 2 (JSON attempt + legacy retry)", rec.calls)
	}
	if got := rec.headers.Get("Target"); got != "Topo::Refer%C3%AAncias" {
		t.Fatalf("Target = %q, want each segment percent-encoded around the delimiter", got)
	}
	if got := rec.headers.Get("Operation"); got != "append" {
		t.Fatalf("Operation = %q, want append", got)
	}
	if rec.body != "novo texto" {
		t.Fatalf("legacy body = %q, want the raw content", rec.body)
	}
}

func TestHandleUpdateNoteFailureIsNotRetried(t *testing.T) {
	var calls int
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorCode":40400,"message":"Not found"}`))
	})

	handler := handleUpdateNote(client)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
		"path":        "notes/missing.md",
		"content":     "x",
		"target_type": "frontmatter",
		"target":      "status",
	})}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected the 404 to surface as an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1: only missing-header 400s are retried", calls)
	}
}

func TestHandleListNotesPathAlias(t *testing.T) {
	var gotPath string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	})

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
		"path": "01-daily",
	})}}
	_, _ = handleListNotes(client)(context.Background(), req)
	if gotPath != "/vault/01-daily" {
		t.Fatalf("path = %q, want /vault/01-daily: \"path\" must not fall back to the root", gotPath)
	}
}

// docMapServer answers document-map requests with the given headings and records
// every other request.
type docMapServer struct {
	headings any
	exists   bool
	methods  []string
	paths    []string
}

func (d *docMapServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Accept") == "application/vnd.olrapi.document-map+json" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"headings": d.headings})
		return
	}
	d.methods = append(d.methods, r.Method)
	d.paths = append(d.paths, r.URL.EscapedPath())
	if r.Method == http.MethodGet && !d.exists {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorCode":40400,"message":"Not found"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func TestResolveHeading(t *testing.T) {
	headings := []string{"Topo", "Topo::Referências", "Topo::Decisões", "Outro::Decisões"}

	t.Run("bare heading expands to its full path", func(t *testing.T) {
		srv := &docMapServer{headings: headings}
		got, err := resolveHeading(newTestClient(t, srv.handler), "n.md", "Referências", "")
		if err != nil || got != "Topo::Referências" {
			t.Fatalf("got %q, %v; want Topo::Referências", got, err)
		}
	})

	t.Run("full path is kept as given", func(t *testing.T) {
		srv := &docMapServer{headings: headings}
		got, err := resolveHeading(newTestClient(t, srv.handler), "n.md", "Outro::Decisões", "")
		if err != nil || got != "Outro::Decisões" {
			t.Fatalf("got %q, %v; want Outro::Decisões", got, err)
		}
	})

	t.Run("ambiguous heading is an error listing the options", func(t *testing.T) {
		srv := &docMapServer{headings: headings}
		_, err := resolveHeading(newTestClient(t, srv.handler), "n.md", "Decisões", "")
		if err == nil || !strings.Contains(err.Error(), "Topo::Decisões") || !strings.Contains(err.Error(), "Outro::Decisões") {
			t.Fatalf("err = %v, want ambiguity listing both paths", err)
		}
	})

	t.Run("plugin 5.x heading tree", func(t *testing.T) {
		tree := map[string]any{
			"Topo":  map[string]any{"Referências": map[string]any{}, "Decisões": map[string]any{}},
			"Outro": map[string]any{"Decisões": map[string]any{}},
		}
		srv := &docMapServer{headings: tree}
		got, err := resolveHeading(newTestClient(t, srv.handler), "n.md", "Referências", "")
		if err != nil || got != "Topo::Referências" {
			t.Fatalf("got %q, %v; want Topo::Referências", got, err)
		}
		_, err = resolveHeading(newTestClient(t, srv.handler), "n.md", "Decisões", "")
		if err == nil || !strings.Contains(err.Error(), "Outro::Decisões | Topo::Decisões") {
			t.Fatalf("err = %v, want sorted ambiguity", err)
		}
	})

	t.Run("unknown heading is left for the API to judge", func(t *testing.T) {
		srv := &docMapServer{headings: headings}
		got, err := resolveHeading(newTestClient(t, srv.handler), "n.md", "Nova", "")
		if err != nil || got != "Nova" {
			t.Fatalf("got %q, %v; want Nova unchanged", got, err)
		}
	})
}

func TestHandleReadNoteResolvesBareHeading(t *testing.T) {
	srv := &docMapServer{headings: []string{"Topo", "Topo::Referências"}, exists: true}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(map[string]any{
		"path":        "n.md",
		"target_type": "heading",
		"target":      "Referências",
	})}}
	result, _ := handleReadNote(newTestClient(t, srv.handler))(context.Background(), req)
	if result.IsError {
		t.Fatalf("unexpected error: %s", getText(result))
	}
	if want := "/vault/n.md/heading/Topo/Refer%C3%AAncias"; len(srv.paths) != 1 || srv.paths[0] != want {
		t.Fatalf("paths = %v, want [%s]", srv.paths, want)
	}
}

func TestHandleUpdateNoteRequiresOperation(t *testing.T) {
	call := func(srv *docMapServer, args map[string]any) *mcp.CallToolResult {
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: mustMarshalArgs(args)}}
		result, err := handleUpdateNote(newTestClient(t, srv.handler))(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return result
	}

	t.Run("heading target without operation is refused", func(t *testing.T) {
		srv := &docMapServer{headings: []string{"Topo", "Topo::Referências"}, exists: true}
		result := call(srv, map[string]any{"path": "n.md", "content": "x", "target_type": "heading", "target": "Topo::Referências"})
		if !result.IsError || !strings.Contains(getText(result), "operation is required") {
			t.Fatalf("want refusal, got %q", getText(result))
		}
		if len(srv.methods) != 0 {
			t.Fatalf("no write expected, got %v", srv.methods)
		}
	})

	t.Run("existing note without operation is not overwritten", func(t *testing.T) {
		srv := &docMapServer{exists: true}
		result := call(srv, map[string]any{"path": "n.md", "content": "x"})
		if !result.IsError || !strings.Contains(getText(result), "already exists") {
			t.Fatalf("want refusal, got %q", getText(result))
		}
		for _, m := range srv.methods {
			if m != http.MethodGet {
				t.Fatalf("no write expected, got %v", srv.methods)
			}
		}
	})

	t.Run("new note without operation is created", func(t *testing.T) {
		srv := &docMapServer{exists: false}
		result := call(srv, map[string]any{"path": "n.md", "content": "x"})
		if result.IsError {
			t.Fatalf("unexpected error: %s", getText(result))
		}
		if srv.methods[len(srv.methods)-1] != http.MethodPut {
			t.Fatalf("methods = %v, want a final PUT", srv.methods)
		}
		if got := getText(result); got != "Successfully replaced note: n.md" {
			t.Fatalf("message = %q", got)
		}
	})

	t.Run("explicit replace overwrites an existing note", func(t *testing.T) {
		srv := &docMapServer{exists: true}
		result := call(srv, map[string]any{"path": "n.md", "content": "x", "operation": "replace"})
		if result.IsError {
			t.Fatalf("unexpected error: %s", getText(result))
		}
		if len(srv.methods) != 1 || srv.methods[0] != http.MethodPut {
			t.Fatalf("methods = %v, want only PUT", srv.methods)
		}
	})

	t.Run("bare heading is resolved before patching", func(t *testing.T) {
		srv := &docMapServer{headings: []string{"Topo", "Topo::Referências"}, exists: true}
		result := call(srv, map[string]any{"path": "n.md", "content": "x", "operation": "append", "target_type": "heading", "target": "Referências"})
		if result.IsError {
			t.Fatalf("unexpected error: %s", getText(result))
		}
		if got := getText(result); got != "Successfully appended note: n.md" {
			t.Fatalf("message = %q", got)
		}
	})
}

func TestRegisterToolsDisableDelete(t *testing.T) {
	for _, disable := range []bool{false, true} {
		s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
		RegisterTools(s, newTestClient(t, func(http.ResponseWriter, *http.Request) {}), &config.Config{DisableDelete: disable})

		ct, st := mcp.NewInMemoryTransports()
		if _, err := s.Connect(context.Background(), st, nil); err != nil {
			t.Fatal(err)
		}
		c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "c"}, nil)
		cs, err := c.Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range res.Tools {
			if tool.Name == "delete_note" {
				found = true
			}
		}
		if found == disable {
			t.Fatalf("DisableDelete=%v: delete_note registered=%v", disable, found)
		}
		_ = cs.Close()
	}
}
