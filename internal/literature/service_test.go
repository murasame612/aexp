package literature

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestClientCatalogStatusAndQuery(t *testing.T) {
	var healthRevision string
	var queryRevision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users/0/collections":
			w.Header().Set("Last-Modified-Version", "321")
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"key": "ROOT", "data": map[string]interface{}{"key": "ROOT", "name": "Methods", "parentCollection": false}},
				{"key": "CHILD", "data": map[string]interface{}{"key": "CHILD", "name": "Time-series RAG", "parentCollection": "ROOT"}},
			})
		case "/health":
			healthRevision = r.URL.Query().Get("corpus_revision")
			if r.Header.Get("Authorization") != "Bearer secret" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ready", "corpus_revision": "corpus_test", "zotero_collection_key": "CHILD", "freshness": "fresh",
				"backends": map[string]interface{}{"paperqa2": map[string]interface{}{"documents": 12, "chunks": 345}},
			})
		case "/query":
			var input map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			queryRevision, _ = input["corpus_revision"].(string)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"answer": "A pinned answer", "answerability": "supported", "corpus_revision": "corpus_test", "zotero_collection_key": "CHILD",
				"evidence": []interface{}{map[string]interface{}{"zotero_item_key": "ITEM", "zotero_uri": "zotero://select/library/items/ITEM", "chunk_sha256": "abc"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "profiles.json")
	config := map[string]interface{}{"profiles": map[string]interface{}{"test": map[string]interface{}{
		"endpoint": server.URL, "token_file": tokenPath, "collection_key": "CHILD", "corpus_revision": "corpus_test",
	}}}
	encoded, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	client := &Client{ProfilesPath: configPath, ZoteroAPI: server.URL + "/api/users/0", HTTPClient: server.Client()}
	catalog, err := client.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.LibraryVersion != 321 || len(catalog.Collections) != 2 || catalog.Collections[1].Path != "Methods / Time-series RAG" {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog.Profiles) != 1 || catalog.Profiles[0].CollectionKey != "CHILD" || catalog.Profiles[0].Documents != 12 {
		t.Fatalf("profiles = %#v", catalog.Profiles)
	}

	project := &store.ProjectDefinition{ID: "project", ZoteroCollectionKey: "CHILD", LiteratureServiceProfile: "test"}
	status, err := client.Status(context.Background(), project, time.Second)
	if err != nil || status["status"] != "ready" {
		t.Fatalf("status = %#v err=%v", status, err)
	}
	query, err := client.Query(context.Background(), project, QueryRequest{Query: "What transfers?"}, time.Second)
	if err != nil || query["answer"] != "A pinned answer" || query["claim_scope"] != "background_only" {
		t.Fatalf("query = %#v err=%v", query, err)
	}
	if healthRevision != "corpus_test" || queryRevision != "corpus_test" {
		t.Fatalf("revision routing health=%q query=%q", healthRevision, queryRevision)
	}
}

func TestClientStatusExplainsUnboundProject(t *testing.T) {
	status, err := NewClient().Status(context.Background(), &store.ProjectDefinition{ID: "project"}, time.Second)
	if err != nil || status["code"] != "LITERATURE_NOT_BOUND" {
		t.Fatalf("status = %#v err=%v", status, err)
	}
}
