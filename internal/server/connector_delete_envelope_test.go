package server

import (
 "encoding/json"
 "net/http"
 "os"
 "path/filepath"
 "testing"
)

func TestConnectorDeleteInUseResponseEnvelope(t *testing.T) {
 f:=agentConnectorsFixture(t)
 rec:=deleteConnectorRequest(f.server,"docs")
 if rec.Code!=http.StatusConflict {t.Fatalf("expected conflict, got %d",rec.Code)}
 var wire struct {Code int `json:"code"`; Msg string `json:"msg"`; Data struct {Error struct {AgentKeys []string `json:"agentKeys"`} `json:"error"`} `json:"data"`}
 if err:=json.Unmarshal(rec.Body.Bytes(),&wire);err!=nil {t.Fatal(err)}
 if wire.Code!=409||wire.Msg==""||len(wire.Data.Error.AgentKeys)!=1||wire.Data.Error.AgentKeys[0]!="mock-agent" {t.Fatalf("unexpected wire envelope: %s",rec.Body.String())}
 if _,err:=os.Stat(filepath.Join(f.server.connectorSources().ExternalRoot,"docs","connector.json"));err!=nil {t.Fatalf("in-use package must remain: %v",err)}
 t.Log(rec.Body.String())
}
