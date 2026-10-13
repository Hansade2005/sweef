//go:build windows

package main

import "os/exec"

func procGroup(*exec.Cmd) {}

func killGroup(c *exec.Cmd) { c.Process.Kill() }
