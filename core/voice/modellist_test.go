package voice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOrderModelsPutsTheDefaultFirstThenByName(t *testing.T) {
	got := OrderModels([]string{"b", "def", "", "a", "b"}, "def")
	if !reflect.DeepEqual(got, []string{"def", "a", "b"}) {
		t.Fatal(got)
	}
	if got := OrderModels([]string{"b", "a"}, "def"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatal("a default the API doesn't list isn't added:", got)
	}
}

func getJSON(t *testing.T, h http.HandlerFunc, header http.Header) (map[string]any, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var out map[string]any
	err := GetJSON(context.Background(), srv.Client(), "x models", srv.URL+"/v1/models", header, &out)
	return out, err
}

func TestGetJSONSendsTheHeaderAndDecodes(t *testing.T) {
	out, err := getJSON(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-1" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}, http.Header{"Authorization": {"Bearer sk-1"}})
	if err != nil || out["ok"] != true {
		t.Fatal(out, err)
	}
}

// An HTTP error says the status and the API's message; the key it echoes is blanked out.
func TestGetJSONErrorsAreShortAndHoldNoKey(t *testing.T) {
	_, err := getJSON(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-secret-1."}}`))
	}, http.Header{"Authorization": {"Bearer sk-secret-1"}})
	if err == nil || err.Error() != "x models: http 401: Incorrect API key provided: ***." {
		t.Fatal(err)
	}
	_, err = getJSON(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		w.Write([]byte(`<html>bad gateway</html>`))
	}, nil)
	if err == nil || err.Error() != "x models: http 502" {
		t.Fatal(err)
	}
	_, err = getJSON(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"` + strings.Repeat("m", 500) + `"}}`))
	}, nil)
	if err == nil || len(err.Error()) > 260 || !strings.HasSuffix(err.Error(), "…") {
		t.Fatal("a long message is clipped:", err)
	}
}

func TestGetJSONBadReply(t *testing.T) {
	_, err := getJSON(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[`)) }, nil)
	if err == nil || err.Error() != "x models: unreadable reply" {
		t.Fatal(err)
	}
}

func TestGetJSONTransportErrorsHoldNoURL(t *testing.T) {
	var out any
	err := GetJSON(context.Background(), http.DefaultClient, "x models", "http://127.0.0.1:1/v1/models?pageToken=tok", nil, &out)
	if err == nil || strings.Contains(err.Error(), "127.0.0.1:1/v1") || strings.Contains(err.Error(), "tok") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	if err := GetJSON(ctx, srv.Client(), "x models", srv.URL, nil, &out); err == nil || err.Error() != "x models: timed out" {
		t.Fatal(err)
	}
}
