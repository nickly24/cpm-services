// Offline JSON canonicalization for the migration rehearsal. No database or
// network configuration is loaded. Private input/output must never be logged.
package main

import (
	"bytes"
	"crypto/sha256"
	"cpm/ratings/internal/ratings"
	"encoding/hex"
	"encoding/json"
	"os"
)

type record struct {
	Input ratings.Input `json:"input"`
	Output ratings.Result `json:"output"`
	InputHash string `json:"input_hash"`
	OutputHash string `json:"output_hash"`
}
func canonical(v any, omit string) string {
	raw,_:=json.Marshal(v)
	d:=json.NewDecoder(bytes.NewReader(raw));d.UseNumber()
	var obj map[string]any;_ = d.Decode(&obj)
	delete(obj,omit);raw,_=json.Marshal(obj)
	h:=sha256.Sum256(raw);return hex.EncodeToString(h[:])
}
func main(){
	var records []record
	if json.NewDecoder(os.Stdin).Decode(&records)!=nil{os.Exit(1)}
	for i:=range records{
		r:=&records[i];r.InputHash=canonical(r.Input,"input_hash")
		r.Input.InputHash=r.InputHash;r.Output.InputHash=r.InputHash
		r.OutputHash=canonical(r.Output,"")
	}
	if json.NewEncoder(os.Stdout).Encode(records)!=nil{os.Exit(1)}
}
