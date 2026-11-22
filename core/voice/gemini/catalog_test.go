package gemini

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

type listedModel struct {
	Name    string   `json:"name"`
	Methods []string `json:"supportedGenerationMethods"`
}

// listed is Gemini's /v1beta/models on 2026-11-22 (name and methods only).
func listed(t *testing.T) []listedModel {
	t.Helper()
	b, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatal(err)
	}
	var ms []listedModel
	if err := json.Unmarshal(b, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

// The Live models of the real list, in ListModels' order.
var liveModels = []string{"gemini-3.8-live", "gemini-2.5-flash-native-audio-latest",
	"gemini-2.5-flash-native-audio-preview-09-2025", "gemini-2.5-flash-native-audio-preview-12-2025",
	"gemini-3.1-flash-live-preview", "gemini-3.8-live-extended-thinking"}

// The bidi models that aren't conversations: the transcriber and translator, robotics, Genie.
func TestTheFamilyLeavesOutTheBidiModelsThatArentConversations(t *testing.T) {
	var kept, dropped []string
	for _, m := range listed(t) {
		id, ok := GeminiModel(m.Name)
		switch {
		case ok:
			kept = append(kept, id)
		case strings.Contains(strings.Join(m.Methods, ","), "bidiGenerateContent"):
			dropped = append(dropped, id)
		}
	}
	if !reflect.DeepEqual(voice.OrderModels(kept, DefaultModel), liveModels) {
		t.Fatal(kept)
	}
	want := []string{"gemini-3.5-transcribe-live", "genie-4", "gemini-robotics-er-2-streaming-preview", "gemini-3.5-live-translate-preview"}
	if !reflect.DeepEqual(dropped, want) {
		t.Fatal(dropped)
	}
	if c, subs := Resolve(modelCfg("gemini-2.5-flash", "Puck")); c.Model != DefaultModel || len(subs) != 1 {
		t.Fatal("a text model is replaced:", c.Model, subs)
	}
}

func listServer(t *testing.T, h http.HandlerFunc) *model {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := newModel("", pingEvery, idleDeadline)
	m.apiURL, m.client = srv.URL, srv.Client()
	return m
}

// Two pages of the real list; the key is a header, never in the URL.
func TestListModelsFollowsPagesAndSendsTheKeyInTheHeader(t *testing.T) {
	ms := listed(t)
	half := len(ms) / 2
	var tokens []string
	m := listServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1beta/models" || q.Get("pageSize") != "1000" || r.Header.Get("x-goog-api-key") != "g-key" ||
			strings.Contains(r.URL.String(), "g-key") {
			w.WriteHeader(403)
			return
		}
		tokens = append(tokens, q.Get("pageToken"))
		if q.Get("pageToken") == "" {
			json.NewEncoder(w).Encode(map[string]any{"models": ms[:half], "nextPageToken": "p2"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"models": ms[half:]})
	})
	got, err := m.ListModels(context.Background(), "g-key")
	if err != nil || !reflect.DeepEqual(got, liveModels) {
		t.Fatal(got, err)
	}
	if !reflect.DeepEqual(tokens, []string{"", "p2"}) {
		t.Fatal(tokens)
	}
}

// A live-sounding model that can't do bidiGenerateContent isn't offered.
func TestListModelsWantsBidi(t *testing.T) {
	m := listServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []listedModel{
			{Name: "models/gemini-9-live", Methods: []string{"generateContent"}},
			{Name: "models/gemini-9-live-2", Methods: []string{"bidiGenerateContent"}},
		}, "nextPageToken": ""})
	})
	if got, err := m.ListModels(context.Background(), "k"); err != nil || !reflect.DeepEqual(got, []string{"gemini-9-live-2"}) {
		t.Fatal(got, err)
	}
}

func TestListModelsStopsOnARepeatedPageToken(t *testing.T) {
	n := 0
	m := listServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		json.NewEncoder(w).Encode(map[string]any{"models": []listedModel{}, "nextPageToken": "same"})
	})
	if _, err := m.ListModels(context.Background(), "k"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

func TestListModelsErrorsAreShortAndHoldNoKey(t *testing.T) {
	m := listServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`))
	})
	_, err := m.ListModels(context.Background(), "g-secret")
	if err == nil || err.Error() != "gemini models: http 400: API key not valid. Please pass a valid API key." {
		t.Fatal(err)
	}
	m = listServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"models":`)) })
	if _, err := m.ListModels(context.Background(), "g-secret"); err == nil || err.Error() != "gemini models: unreadable reply" {
		t.Fatal(err)
	}
}
