package spatial

import (
	"encoding/json"
	"sort"
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

// CalculateAcres calculates the area in acres, assuming the geometry is in a
// coordinate system whose unit is feet (e.g. EPSG:2278)
func CalculateAcres(geom orb.Geometry) float64 {
	return planar.Area(geom) / 43560.0
}

// PointOnSurface returns a point guaranteed to lie inside the geometry.
// The centroid alone is not enough: on concave (L/U-shaped) parcels it can
// fall outside the polygon, which would join the parcel to no district or to
// the wrong one. When that happens, a horizontal scanline through the
// centroid's Y collects edge crossings and probes the midpoint of each
// crossing span until one tests inside.
func PointOnSurface(geom orb.Geometry) orb.Point {
	centroid, _ := planar.CentroidArea(geom)
	if Contains(geom, centroid) {
		return centroid
	}

	y := centroid[1]
	var xs []float64
	eachRing(geom, func(r orb.Ring) {
		for i := 0; i < len(r)-1; i++ {
			p1, p2 := r[i], r[i+1]
			if (p1[1] > y) != (p2[1] > y) {
				xs = append(xs, p1[0]+(y-p1[1])/(p2[1]-p1[1])*(p2[0]-p1[0]))
			}
		}
	})
	sort.Float64s(xs)
	for i := 0; i+1 < len(xs); i++ {
		mid := orb.Point{(xs[i] + xs[i+1]) / 2, y}
		if Contains(geom, mid) {
			return mid
		}
	}
	return centroid
}

func eachRing(geom orb.Geometry, fn func(orb.Ring)) {
	switch g := geom.(type) {
	case orb.Polygon:
		for _, r := range g {
			fn(r)
		}
	case orb.MultiPolygon:
		for _, p := range g {
			for _, r := range p {
				fn(r)
			}
		}
	}
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

// IsResidential applies the brief's rule: a district is residential when its
// code begins with "R" (R1, RM, R-1) or its name begins with the word
// "Residential". The name check is deliberately NOT "begins with R" — Buda
// really has a PD district named "Reserve at Cole Springs" that a bare
// starts-with-R name check would misclassify as residential.
func IsResidential(code, name string) bool {
	c := strings.ToUpper(strings.TrimSpace(code))
	n := strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(c, "R") || strings.HasPrefix(n, "RESIDENTIAL")
}
