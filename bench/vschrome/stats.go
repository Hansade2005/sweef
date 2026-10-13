//go:build linux

package main

import (
	"fmt"
	"math"
	"sort"
)

func sorted(v []float64) []float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := sorted(v)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func minOf(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	return sorted(v)[0]
}

func maxOf(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := sorted(v)
	return s[len(s)-1]
}

// summary is "median (min-max)" of v with the given format for each number.
func summary(v []float64, f string) string {
	if len(v) == 0 {
		return "n/a"
	}
	return fmt.Sprintf(f+" ("+f+"–"+f+")", median(v), minOf(v), maxOf(v))
}

// overlap reports whether the [min,max] ranges of a and b overlap.
func overlap(a, b []float64) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	return minOf(a) <= maxOf(b) && minOf(b) <= maxOf(a)
}
