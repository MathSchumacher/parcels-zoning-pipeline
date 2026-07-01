package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
)

type Feature struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Geometry   json.RawMessage        `json:"geometry"`
}

type FeatureCollection struct {
	Type                  string    `json:"type"`
	Features              []Feature `json:"features"`
	ExceededTransferLimit bool      `json:"exceededTransferLimit"`
}

// FetchLayer handles paginated fetching from an ArcGIS REST API /query endpoint
func FetchLayer(baseURL string, orderBy string, outSR string) ([]Feature, error) {
	var allFeatures []Feature
	offset := 0
	limit := 2000

	client := &http.Client{Timeout: 60 * time.Second}

	for {
		reqURL, err := url.Parse(baseURL + "/query")
		if err != nil {
			return nil, err
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
			return nil, fmt.Errorf("failed fetching offset %d: %w", offset, err)
		}

		allFeatures = append(allFeatures, fc.Features...)

		if !fc.ExceededTransferLimit || len(fc.Features) == 0 {
			break
		}
		offset += limit
	}

	return allFeatures, nil
}
