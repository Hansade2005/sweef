//go:build 386 || arm

package engine

func installBytecodeLoader(j *jsVM) bool { return false }
func uninstallBytecodeLoader(j *jsVM)    {}
