package raster

import "strconv"

func parseFloat(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
