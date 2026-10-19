package shellenv

import "os/exec"

func detach(*exec.Cmd)          {}
func killGroup(*exec.Cmd)       {}
func detectShell(string) string { return "" }
