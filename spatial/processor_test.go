package spatial

import (
	"testing"
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

	centroid := GetCentroid(parcelGeom)
	if !Contains(zoneGeom, centroid) {
		t.Errorf("Expected L-shaped parcel centroid to be contained in zone")
	}
}

func TestIsResidential(t *testing.T) {
	tests := []struct {
		code     string
		name     string
		expected bool
	}{
		{"R-1", "", true},
		{"", "Residential - Single Family", true},
		{" RM ", "", true},
		{"C-1", "Commercial", false},
		{"", "", false},
	}

	for _, tt := range tests {
		result := IsResidential(tt.code, tt.name)
		if result != tt.expected {
			t.Errorf("IsResidential('%s', '%s') = %v, expected %v", tt.code, tt.name, result, tt.expected)
		}
	}
}
