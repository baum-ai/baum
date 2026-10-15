//go:build !windows

package uiserver

import (
 "errors"
 "os"
)
func protectDirectory(path string)error{info,err:=os.Lstat(path);if err!=nil{return err};if !info.IsDir()||info.Mode().Perm()&0077!=0{return errors.New("ui instance directory must be private (0700)")};return nil}
func privateFile(_ string,info os.FileInfo)bool{return info.Mode().IsRegular()&&info.Mode().Perm()&0077==0}
