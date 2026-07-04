package service

import (
	"reflect"
	"testing"
)

func TestParseModelCatalog(t *testing.T) {
	body := []byte(`{
		"object":"list",
		"data":[
			{"id":"gpt-image-2"},
			{"id":"gpt-4o-mini"},
			{"id":"gpt-image-2"},
			{"id":" "}
		]
	}`)

	got := parseModelCatalog(body)
	want := []string{"gpt-4o-mini", "gpt-image-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModelCatalog() = %v, want %v", got, want)
	}
}
