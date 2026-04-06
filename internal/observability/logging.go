package observability

import (
	"encoding/json"
	"log"
)

func Log(category string, fields map[string]any) {
	payload := map[string]any{"category": category}
	for key, value := range fields {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[obs][%s] marshal_error=%v", category, err)
		return
	}
	log.Printf("%s", data)
}
