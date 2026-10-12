//go:build windows

package main

import "os/exec"

func groupAttr(*exec.Cmd) {}

func killGroup(c *exec.Cmd) { c.Process.Kill() }
