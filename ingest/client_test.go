package ingest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func mockCRS(sr string) *CRS {
	return &CRS{Type: "name", Properties: CRSProperties{Name: "EPSG:" + sr}}
}

func TestFetchLayer_Pagination(t *testing.T) {
	requestCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		offsetStr := r.URL.Query().Get("resultOffset")
		offset, _ := strconv.Atoi(offsetStr)

		fc := FeatureCollection{
			Type: "FeatureCollection",
			CRS:  mockCRS("2278"),
		}

		if offset == 0 {
			// First page. The real service nests the flag under "properties"
			// when f=geojson; the mock must mirror that shape.
			fc.Features = []Feature{
				{Type: "Feature", Properties: map[string]interface{}{"OBJECTID": 1}},
				{Type: "Feature", Properties: map[string]interface{}{"OBJECTID": 2}},
			}
			fc.Properties = &FCProperties{ExceededTransferLimit: true}
		} else {
			// Second page
			fc.Features = []Feature{
				{Type: "Feature", Properties: map[string]interface{}{"OBJECTID": 3}},
			}
			fc.ExceededTransferLimit = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fc)
	}))
	defer ts.Close()

	features, err := FetchLayer(ts.URL, "OBJECTID", "2278")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(features) != 3 {
		t.Errorf("expected 3 features, got %d", len(features))
	}

	if requestCount != 2 {
		t.Errorf("expected 2 requests for pagination, got %d", requestCount)
	}
}

func TestFetchLayer_RetryBackoff(t *testing.T) {
	requestCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if requestCount == 1 {
			// Simulate 500 server error on first try
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Succeed on second try
		fc := FeatureCollection{
			Type: "FeatureCollection",
			CRS:  mockCRS("2278"),
			Features: []Feature{
				{Type: "Feature", Properties: map[string]interface{}{"OBJECTID": 1}},
			},
			ExceededTransferLimit: false,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fc)
	}))
	defer ts.Close()

	features, err := FetchLayer(ts.URL, "OBJECTID", "2278")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(features) != 1 {
		t.Errorf("expected 1 feature, got %d", len(features))
	}

	if requestCount != 2 {
		t.Errorf("expected 2 requests (1 fail, 1 success), got %d", requestCount)
	}
}

// A service that ignores outSR and answers in another CRS (or omits it, the
// WGS84 default) must abort the fetch: areas computed in the wrong units are
// silently wrong, not visibly broken.
func TestFetchLayer_CRSGuard(t *testing.T) {
	requestCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		fc := FeatureCollection{
			Type: "FeatureCollection",
			// No CRS: what AGOL returns when it falls back to WGS84.
			Features: []Feature{
				{Type: "Feature", Properties: map[string]interface{}{"OBJECTID": 1}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fc)
	}))
	defer ts.Close()

	_, err := FetchLayer(ts.URL, "OBJECTID", "2278")
	if err == nil || !strings.Contains(err.Error(), "CRS") {
		t.Fatalf("expected CRS guard error, got %v", err)
	}

	if requestCount != 1 {
		t.Errorf("CRS mismatch should not be retried; got %d requests", requestCount)
	}
}
