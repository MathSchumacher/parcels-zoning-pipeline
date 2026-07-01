package spatial

import (
	"encoding/json"
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/planar"
)

// ParseGeometry parses a raw GeoJSON geometry into an orb.Geometry
func ParseGeometry(geomJSON []byte) (orb.Geometry, error) {
	var g geojson.Geometry
	if err := json.Unmarshal(geomJSON, &g); err != nil {
		return nil, err
	}
	return g.Geometry(), nil
}

// CalculateAcres calculates the area in acres, assuming the geometry is in a coordinate system where units are feet (e.g. EPSG:2277)
func CalculateAcres(geom orb.Geometry) float64 {
	return planar.Area(geom) / 43560.0
}

// GetCentroid returns the area-weighted centroid of the geometry (acts as PointOnSurface approximation)
func GetCentroid(geom orb.Geometry) orb.Point {
	return planar.CentroidArea(geom)
}

// Contains checks if a point is inside a Polygon or MultiPolygon
func Contains(polygonGeom orb.Geometry, point orb.Point) bool {
	switch g := polygonGeom.(type) {
	case orb.Polygon:
		return planar.PolygonContains(g, point)
	case orb.MultiPolygon:
		return planar.MultiPolygonContains(g, point)
	}
	return false
}

// IsResidential checks if the code or name indicates a residential zoning category
func IsResidential(code, name string) bool {
	c := strings.TrimSpace(strings.ToUpper(code))
	n := strings.TrimSpace(strings.ToUpper(name))
	
	if c == "" && n == "" {
		return false
	}
	
	// Check if either code or name starts with 'R' or 'RESIDENTIAL'
	if (len(c) > 0 && c[0] == 'R') || (len(n) > 0 && n[0] == 'R') {
		return true
	}
	return false
}
