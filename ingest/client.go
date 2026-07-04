package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

type Feature struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Geometry   json.RawMessage        `json:"geometry"`
}

type CRSProperties struct {
	Name string `json:"name"`
}

type CRS struct {
	Type       string        `json:"type,omitempty"`
	Properties CRSProperties `json:"properties"`
}

// FCProperties is the top-level "properties" object ArcGIS adds to GeoJSON
// responses. With f=geojson the exceededTransferLimit flag lives HERE, not at
// the top level like it does with f=json — miss that and pagination silently
// stops after the first page.
type FCProperties struct {
	ExceededTransferLimit bool `json:"exceededTransferLimit"`
}

type FeatureCollection struct {
	Type                  string        `json:"type"`
	CRS                   *CRS          `json:"crs,omitempty"`
	Properties            *FCProperties `json:"properties,omitempty"`
	Features              []Feature     `json:"features"`
	ExceededTransferLimit bool          `json:"exceededTransferLimit"`
}

func (fc *FeatureCollection) exceededLimit() bool {
	return fc.ExceededTransferLimit || (fc.Properties != nil && fc.Properties.ExceededTransferLimit)
}

// FetchLayerPages pages through an ArcGIS REST API /query endpoint, invoking
// onPage for each page so large layers never have to fit in memory at once.
// Every page is checked against the requested spatial reference: computing
// areas in the wrong units is a silent-corruption failure mode, so a CRS
// mismatch aborts the fetch loudly instead.
func FetchLayerPages(baseURL string, orderBy string, outSR string, onPage func([]Feature) error) error {
	offset := 0
	limit := 2000

	client := &http.Client{Timeout: 60 * time.Second}

	for {
		reqURL, err := url.Parse(baseURL + "/query")
		if err != nil {
			return err
		}

		q := reqURL.Query()
		q.Set("where", "1=1")
		q.Set("outFields", "*")
		q.Set("f", "geojson")
		q.Set("outSR", outSR)
		q.Set("orderByFields", orderBy)
		q.Set("resultOffset", strconv.Itoa(offset))
		q.Set("resultRecordCount", strconv.Itoa(limit))

		reqURL.RawQuery = q.Encode()
		fmt.Printf("Fetching offset %d...\n", offset)

		var fc FeatureCollection

		operation := func() error {
			resp, err := client.Get(reqURL.String())
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				return fmt.Errorf("temporary error: %d", resp.StatusCode)
			}
			if resp.StatusCode != 200 {
				return backoff.Permanent(fmt.Errorf("fatal error: %d", resp.StatusCode))
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}

			if err := json.Unmarshal(body, &fc); err != nil {
				return backoff.Permanent(fmt.Errorf("json unmarshal error: %w", err))
			}
			return nil
		}

		bo := backoff.NewExponentialBackOff()
		bo.MaxElapsedTime = 2 * time.Minute

		if err := backoff.Retry(operation, bo); err != nil {
			return fmt.Errorf("failed fetching offset %d: %w", offset, err)
		}

		if err := checkCRS(fc.CRS, outSR); err != nil {
			return err
		}

		if err := onPage(fc.Features); err != nil {
			return err
		}

		if !fc.exceededLimit() || len(fc.Features) == 0 {
			return nil
		}
		offset += limit
	}
}

// FetchLayer fetches every page into memory. Prefer FetchLayerPages for
// large layers.
func FetchLayer(baseURL string, orderBy string, outSR string) ([]Feature, error) {
	var all []Feature
	err := FetchLayerPages(baseURL, orderBy, outSR, func(page []Feature) error {
		all = append(all, page...)
		return nil
	})
	return all, err
}

// checkCRS guards the units contract: ArcGIS services are not obliged to
// honor outSR with f=geojson, and a WGS84 fallback would turn every computed
// area into ~zero acres without any error.
func checkCRS(crs *CRS, outSR string) error {
	if crs == nil {
		return fmt.Errorf("response carries no CRS (service likely ignored outSR=%s and returned WGS84); refusing to compute areas in unknown units", outSR)
	}
	if !strings.HasSuffix(crs.Properties.Name, ":"+outSR) {
		return fmt.Errorf("response CRS is %q, expected EPSG:%s; refusing to compute areas in wrong units", crs.Properties.Name, outSR)
	}
	return nil
}
