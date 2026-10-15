package cli

import (
 "strings"
 "testing"
)
func TestUICommandFlags(t *testing.T){out,_,err:=run(t,"ui","--help");if err!=nil||!strings.Contains(out,"--no-open")||!strings.Contains(out,"--port"){t.Fatal(out,err)};for _,port:=range []string{"-1","65536"}{_,_,err=run(t,"ui","--port",port,"--no-open");if err==nil||!strings.Contains(err.Error(),"out of range"){t.Fatal(err)}}}
