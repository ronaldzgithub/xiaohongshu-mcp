package xiaohongshu

import (
	"encoding/json"
	"testing"
)

func TestDetailSnapshotPreservesOnlyObservedAuthorAccess(t *testing.T) {
	for _, test := range []struct {
		name, user, want string
	}{
		{"observed author", `{"userId":"author-one","nickname":"Author","xsecToken":"author-only"}`, "author-only"},
		{"no author access", `{"userId":"author-one","nickname":"Author"}`, ""},
		{"empty author access", `{"userId":"author-one","xsecToken":""}`, ""},
		{"unbound access", `{"nickname":"Author","xsecToken":"unbound"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"note":{"noteId":"fixture","title":"Title","desc":"","type":"normal","xsecToken":"post-only","user":` + test.user + `},"comments":{}}`
			result, err := decodeDetailSnapshot(raw, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			if result.Note.User.XsecToken != test.want || result.Note.XsecToken != "post-only" {
				t.Fatal("author access must come only from the observed author object")
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var output struct {
				Note struct {
					User map[string]any `json:"user"`
				} `json:"note"`
			}
			if err := json.Unmarshal(encoded, &output); err != nil {
				t.Fatal(err)
			}
			value, exists := output.Note.User["xsecToken"]
			if (test.want == "" && exists) || (test.want != "" && value != test.want) {
				t.Fatal("native output must preserve the observed field or omit missing access")
			}
		})
	}
}

func TestLegacyDetailDecodeRetainsAuthorAndOtherUserContractsStayUnchanged(t *testing.T) {
	// The legacy detail extractor also decodes this typed noteDetailMap shape.
	raw := `{"fixture":{"note":{"noteId":"fixture","user":{"userId":"author-one","xsecToken":"author-only"}},"comments":{}}}`
	var details map[string]FeedDetailResponse
	if err := json.Unmarshal([]byte(raw), &details); err != nil || details["fixture"].Note.User.XsecToken != "author-only" {
		t.Fatal("detail decoder lost the observed author access", err)
	}
	var user User
	if err := json.Unmarshal([]byte(`{"userId":"author-one","xsecToken":"author-only"}`), &user); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(user)
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if _, exists := output["xsecToken"]; exists {
		t.Fatal("shared search/comment User contract must remain unchanged")
	}
}
