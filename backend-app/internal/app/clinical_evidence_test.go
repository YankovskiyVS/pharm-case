package app

import (
	"testing"
)

func TestClinicalEvidenceSchemaAndConfig(t *testing.T) {
	schema := clinicalEvidenceSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("strict root schema is required")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["medications"] == nil || properties["monitoring"] == nil {
		t.Fatal("clinical sections missing")
	}
	valid := Config{ClinicalResearchURL: "https://api.openai.com/v1", ClinicalResearchModel: "gpt-6-astra"}
	if err := ValidateClinicalResearchConfig(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	valid.ClinicalResearchURL = "https://example.com/v1"
	if err := ValidateClinicalResearchConfig(valid); err == nil {
		t.Fatal("non-OpenAI research endpoint accepted")
	}
}
