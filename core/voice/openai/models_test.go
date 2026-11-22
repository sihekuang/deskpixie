package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

var _ voice.ModelCatalog = (*model)(nil)

// modelIDs is OpenAI's /v1/models on 2026-11-22 (ids only).
func modelIDs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/model-ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		t.Fatal(err)
	}
	return ids
}

var realtimeModels = []string{"gpt-realtime", "gpt-realtime-1.5", "gpt-realtime-2", "gpt-realtime-2.1",
	"gpt-realtime-2.1-mini", "gpt-realtime-2025-08-28", "gpt-realtime-mini", "gpt-realtime-mini-2025-12-15"}

func TestTheFamilyIsTheConversationModelsOfTheRealList(t *testing.T) {
	var got []string
	for _, id := range modelIDs(t) {
		if IsModel(id) {
			got = append(got, id)
		}
	}
	if !reflect.DeepEqual(got, realtimeModels) {
		t.Fatal(got)
	}
}

// Resolve notes a replacement (logged once by Connect); an empty setting needs none.
func TestResolveReplacesAnotherProvidersModel(t *testing.T) {
	for _, m := range []string{"gpt-live-1", "gemini-3.8-live", "gpt-realtime-translate", "gpt-4o"} {
		c, notes := Resolve(voice.SessionConfig{Model: m})
		if c.Model != DefaultModel || len(notes) != 1 || notes[0] != `voice.openaiModel "`+m+`" is not an OpenAI Realtime model; using gpt-realtime` {
			t.Errorf("%q: %q %q", m, c.Model, notes)
		}
	}
	if c, notes := Resolve(voice.SessionConfig{}); c.Model != DefaultModel || len(notes) != 0 {
		t.Fatal(c.Model, notes)
	}
	if c, notes := Resolve(voice.SessionConfig{Model: " gpt-realtime-2.1 "}); c.Model != "gpt-realtime-2.1" || len(notes) != 0 {
		t.Fatal(c.Model, notes)
	}
}

func TestAnotherProvidersModelNeverReachesOpenAI(t *testing.T) {
	var path string
	srv := start(t, true, func(s *srvConn) { <-s.ctx.Done() }, nil, &path)
	c := cfg()
	c.Model = "gpt-live-1"
	s, err := New(wsURL(srv)).Connect(context.Background(), c, "k")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if path != "/v1/realtime?model=gpt-realtime" {
		t.Fatal(path)
	}
}

func listServer(t *testing.T, h http.HandlerFunc) *model {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := New("").(*model)
	m.apiURL, m.client = srv.URL, srv.Client()
	return m
}

func TestListModelsSendsTheKeyInTheHeaderAndKeepsTheFamily(t *testing.T) {
	ids := modelIDs(t)
	m := listServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-k" || strings.Contains(r.URL.String(), "sk-k") {
			w.WriteHeader(401)
			return
		}
		var data []map[string]string
		for i := len(ids) - 1; i >= 0; i-- { // reversed: the order is ListModels'
			data = append(data, map[string]string{"id": ids[i], "object": "model"})
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	got, err := m.ListModels(context.Background(), "sk-k")
	if err != nil || !reflect.DeepEqual(got, realtimeModels) {
		t.Fatal(got, err)
	}
}

func TestListModelsErrorsAreShortAndHoldNoKey(t *testing.T) {
	m := listServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-secret.","code":"invalid_api_key"}}`))
	})
	_, err := m.ListModels(context.Background(), "sk-secret")
	if err == nil || err.Error() != "openai models: http 401: Incorrect API key provided: ***." {
		t.Fatal(err)
	}
	m = listServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) })
	if _, err := m.ListModels(context.Background(), "sk-secret"); err == nil || err.Error() != "openai models: unreadable reply" {
		t.Fatal(err)
	}
}
