package connector

import (
 "path/filepath"
 "testing"
)

func TestWeComProtocolExample(t *testing.T) {
 pkg,err:=Load(filepath.Join("..","..","examples","connectors"),"wecom-cli-connector")
 if err!=nil {t.Fatal(err)}
 if err:=ValidateExternalCLI(pkg);err!=nil {t.Fatal(err)}
 if !pkg.ManagedCLI() {t.Fatal("WeCom must declare managed login, status, and disconnect")}
 if pkg.AuthMode!=AuthDelegated {t.Fatal("WeCom owns its CLI authentication")}
 if pkg.CLI["platform"]!=nil {t.Fatal("example must not depend on a legacy Platform extension")}
}
