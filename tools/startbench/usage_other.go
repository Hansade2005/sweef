//go:build !linux && !darwin

package main

import "os"

func usage(*os.ProcessState) (rssMiB, cpuMs float64) { return 0, 0 }
