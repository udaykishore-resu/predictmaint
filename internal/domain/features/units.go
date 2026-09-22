package features

import (
	"fmt"
	"strings"
)

// Dimension names a physical quantity. Two units are convertible only when
// they share a dimension; anything else is a hard error, never a silent
// pass-through, because a psi reading interpreted as bar is exactly the kind
// of bug that makes a fleet-wide model useless.
type Dimension string

const (
	DimVelocity      Dimension = "velocity"
	DimAcceleration  Dimension = "acceleration"
	DimTemperature   Dimension = "temperature"
	DimPressure      Dimension = "pressure"
	DimCurrent       Dimension = "current"
	DimSpeed         Dimension = "rotational_speed"
	DimFlow          Dimension = "flow"
	DimRatio         Dimension = "ratio"
	DimTime          Dimension = "time"
	DimDimensionless Dimension = "dimensionless"
)

// unitDef is a linear conversion to the canonical unit of its dimension:
// canonical = value*scale + offset.
type unitDef struct {
	dim    Dimension
	scale  float64
	offset float64
}

// canonicalUnit is the reference unit per dimension.
var canonicalUnit = map[Dimension]string{
	DimVelocity:      "mm/s",
	DimAcceleration:  "g",
	DimTemperature:   "C",
	DimPressure:      "bar",
	DimCurrent:       "A",
	DimSpeed:         "rpm",
	DimFlow:          "m3/h",
	DimRatio:         "%",
	DimTime:          "h",
	DimDimensionless: "",
}

var units = map[string]unitDef{
	// velocity
	"mm/s": {DimVelocity, 1, 0},
	"in/s": {DimVelocity, 25.4, 0},
	"m/s":  {DimVelocity, 1000, 0},
	// acceleration
	"g":     {DimAcceleration, 1, 0},
	"m/s2":  {DimAcceleration, 1 / 9.80665, 0},
	"m/s^2": {DimAcceleration, 1 / 9.80665, 0},
	// temperature
	"c":    {DimTemperature, 1, 0},
	"degc": {DimTemperature, 1, 0},
	"°c":   {DimTemperature, 1, 0},
	"f":    {DimTemperature, 5.0 / 9.0, -32 * 5.0 / 9.0},
	"degf": {DimTemperature, 5.0 / 9.0, -32 * 5.0 / 9.0},
	"°f":   {DimTemperature, 5.0 / 9.0, -32 * 5.0 / 9.0},
	"k":    {DimTemperature, 1, -273.15},
	// pressure
	"bar": {DimPressure, 1, 0},
	"psi": {DimPressure, 0.0689476, 0},
	"kpa": {DimPressure, 0.01, 0},
	"pa":  {DimPressure, 1e-5, 0},
	"mpa": {DimPressure, 10, 0},
	// current
	"a":  {DimCurrent, 1, 0},
	"ma": {DimCurrent, 1e-3, 0},
	// speed
	"rpm": {DimSpeed, 1, 0},
	"hz":  {DimSpeed, 60, 0},
	// flow
	"m3/h":  {DimFlow, 1, 0},
	"l/min": {DimFlow, 0.06, 0},
	"gpm":   {DimFlow, 0.227125, 0},
	// ratio
	"%":     {DimRatio, 1, 0},
	"pct":   {DimRatio, 1, 0},
	"ratio": {DimRatio, 100, 0},
	// time
	"h":   {DimTime, 1, 0},
	"hr":  {DimTime, 1, 0},
	"min": {DimTime, 1.0 / 60.0, 0},
	"s":   {DimTime, 1.0 / 3600.0, 0},
	// dimensionless
	"": {DimDimensionless, 1, 0},
}

func normUnit(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// UnitDimension returns the dimension of a known unit.
func UnitDimension(u string) (Dimension, error) {
	d, ok := units[normUnit(u)]
	if !ok {
		return "", fmt.Errorf("features: unknown unit %q", u)
	}
	return d.dim, nil
}

// Convert converts value from one unit to another. Both units must be known
// and share a dimension.
func Convert(value float64, from, to string) (float64, error) {
	f, ok := units[normUnit(from)]
	if !ok {
		return 0, fmt.Errorf("features: unknown unit %q", from)
	}
	t, ok := units[normUnit(to)]
	if !ok {
		return 0, fmt.Errorf("features: unknown unit %q", to)
	}
	if f.dim != t.dim {
		return 0, fmt.Errorf("features: cannot convert %s (%s) to %s (%s)", from, f.dim, to, t.dim)
	}
	canonical := value*f.scale + f.offset
	return (canonical - t.offset) / t.scale, nil
}

// CanonicalUnit returns the reference unit of the given unit's dimension.
func CanonicalUnit(u string) (string, error) {
	d, err := UnitDimension(u)
	if err != nil {
		return "", err
	}
	return canonicalUnit[d], nil
}
