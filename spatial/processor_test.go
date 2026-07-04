package spatial

import (
	"testing"

	"github.com/paulmach/orb/planar"
)

func TestCalculateAcres(t *testing.T) {
	// A square of 208.71032557 feet per side is exactly 1 acre (43560 sq ft)
	geomJSON := []byte(`{
		"type": "Polygon",
		"coordinates": [[
			[0, 0],
			[208.71032557, 0],
			[208.71032557, 208.71032557],
			[0, 208.71032557],
			[0, 0]
		]]
	}`)
	geom, err := ParseGeometry(geomJSON)
	if err != nil {
		t.Fatalf("ParseGeometry failed: %v", err)
	}
	acres := CalculateAcres(geom)
	if acres < 0.999 || acres > 1.001 {
		t.Errorf("Expected area to be ~1.0 acres, got %f", acres)
	}
}

func TestSpatialJoin_LShape(t *testing.T) {
	// A zoning district from (0,0) to (10,10)
	zoneJSON := []byte(`{
		"type": "Polygon",
		"coordinates": [[
			[0, 0], [10, 0], [10, 10], [0, 10], [0, 0]
		]]
	}`)
	zoneGeom, _ := ParseGeometry(zoneJSON)

	// An L-shaped parcel that overlaps
	parcelJSON := []byte(`{
		"type": "Polygon",
		"coordinates": [[
			[0, 0], [10, 0], [10, 10], [5, 10], [5, 5], [0, 5], [0, 0]
		]]
	}`)
	parcelGeom, _ := ParseGeometry(parcelJSON)

	pt := PointOnSurface(parcelGeom)
	if !Contains(parcelGeom, pt) {
		t.Errorf("Expected point-on-surface to be inside the parcel itself")
	}
	if !Contains(zoneGeom, pt) {
		t.Errorf("Expected L-shaped parcel's point-on-surface to be contained in zone")
	}
}

// A U-shaped parcel whose centroid falls in the notch — outside the polygon.
// This is the case where a bare centroid joins the parcel to the wrong
// district (or none); PointOnSurface must fall back to an interior point.
func TestPointOnSurface_UShape(t *testing.T) {
	parcelJSON := []byte(`{
		"type": "Polygon",
		"coordinates": [[
			[0, 0], [10, 0], [10, 10], [7, 10], [7, 3], [3, 3], [3, 10], [0, 10], [0, 0]
		]]
	}`)
	parcelGeom, _ := ParseGeometry(parcelJSON)

	centroid, _ := planar.CentroidArea(parcelGeom)
	if Contains(parcelGeom, centroid) {
		t.Fatalf("test setup: centroid unexpectedly inside the U; fallback not exercised")
	}

	pt := PointOnSurface(parcelGeom)
	if !Contains(parcelGeom, pt) {
		t.Errorf("Expected point-on-surface (%v) to be inside the U-shaped parcel", pt)
	}
}

func TestIsResidential(t *testing.T) {
	tests := []struct {
		code     string
		name     string
		expected bool
	}{
		{"R-1", "", true},
		{"R1", "Estate Residential", true},
		{"", "Residential - Single Family", true},
		{" RM ", "", true},
		{"C-1", "Commercial", false},
		// Real Buda districts: names starting with "R" that are NOT
		// residential under the brief's rule.
		{"PD", "Reserve at Cole Springs", false},
		// Ambiguity flagged in the design doc: residential in nature, but
		// the brief's rule excludes it (code B2, name starts "Suburban").
		{"B2", "Suburban Residential", false},
		{"F1", "Rural", false},
		{"", "", false},
	}

	for _, tt := range tests {
		result := IsResidential(tt.code, tt.name)
		if result != tt.expected {
			t.Errorf("IsResidential('%s', '%s') = %v, expected %v", tt.code, tt.name, result, tt.expected)
		}
	}
}
