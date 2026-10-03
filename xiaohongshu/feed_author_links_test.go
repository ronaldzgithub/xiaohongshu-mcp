package xiaohongshu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthorAccessUsesOnlyMatchingObservedProfileHref(t *testing.T) {
	correct := "/user/profile/author-one?xsec_token=author-only&xsec_source=pc_note"
	for _, test := range []struct {
		name  string
		links []string
		want  string
	}{
		{"missing", nil, ""},
		{"relative matching", []string{correct}, "author-only"},
		{"absolute matching", []string{"https://www.xiaohongshu.com" + correct}, "author-only"},
		{"duplicate same token", []string{correct, "https://www.xiaohongshu.com" + correct}, "author-only"},
		{"wrong author", []string{"/user/profile/other?xsec_token=other-only"}, ""},
		{"post access", []string{"/explore/author-one?xsec_token=post-only"}, ""},
		{"no access", []string{"/user/profile/author-one"}, ""},
		{"empty access", []string{"/user/profile/author-one?xsec_token="}, ""},
		{"conflicting access", []string{correct, "/user/profile/author-one?xsec_token=conflict"}, ""},
		{"duplicate query", []string{correct + "&xsec_token=author-only"}, ""},
		{"foreign host", []string{"https://other.example" + correct}, ""},
		{"host suffix attack", []string{"https://www.xiaohongshu.com.other.example" + correct}, ""},
		{"non HTTPS", []string{"http://www.xiaohongshu.com" + correct}, ""},
		{"userinfo", []string{"https://other@www.xiaohongshu.com" + correct}, ""},
		{"malformed escape", []string{correct + "%zz"}, ""},
		{"control character", []string{"/user/profile/author-one?xsec_token=author-only%0A"}, ""},
		{"href bound", []string{correct + strings.Repeat("x", 2048)}, ""},
		{"count bound", append(make([]string, 32), correct), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := authorAccessFromLinks("author-one", "https://www.xiaohongshu.com", test.links); got != test.want {
				t.Fatal("observed author link binding result differs")
			}
		})
	}
	for _, origin := range []string{"", "https://other.example", "https://www.xiaohongshu.com/path"} {
		if authorAccessFromLinks("author-one", origin, []string{correct}) != "" {
			t.Fatal("unverified current page origin accepted")
		}
	}
	if authorAccessFromLinks("", "https://www.xiaohongshu.com", []string{correct}) != "" {
		t.Fatal("missing author identity accepted")
	}
}

func TestDetailSnapshotAuthorHrefIsOptionalAndNeverEmitted(t *testing.T) {
	for _, sourceToken := range []string{"", "state-author-only"} {
		input := map[string]any{
			"note": map[string]any{"noteId": "fixture", "title": "Title", "desc": "", "type": "normal", "xsecToken": "post-only",
				"user": map[string]any{"userId": "author-one", "xsecToken": sourceToken}},
			"comments":               map[string]any{},
			"_author_profile_origin": "https://www.xiaohongshu.com",
			"_author_profile_links":  []string{"/user/profile/author-one?xsec_token=link-author-only"},
		}
		raw, _ := json.Marshal(input)
		result, err := decodeDetailSnapshot(string(raw), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		want := sourceToken
		if want == "" {
			want = "link-author-only"
		}
		if result.Note.User.XsecToken != want || result.Note.XsecToken != "post-only" {
			t.Fatal("snapshot must preserve state author access or bind the actual author href")
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "_author_profile") || strings.Contains(string(encoded), "/user/profile/") {
			t.Fatal("temporary href evidence must not leak into the native response")
		}
	}
}
