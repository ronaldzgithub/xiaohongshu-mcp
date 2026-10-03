package xiaohongshu

import (
	"net/url"
	"strings"
)

// authorAccessFromLinks consumes only observed hrefs from the current detail
// page. It never creates a token, borrows a note token, or performs network I/O.
func authorAccessFromLinks(authorID, origin string, links []string) string {
	if authorID == "" || strings.ContainsAny(authorID, "/?#\\") || len(links) > 32 {
		return ""
	}
	base, err := url.Parse(origin)
	if err != nil || !officialProfileOrigin(base) || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return ""
	}
	token := ""
	for _, href := range links {
		if len(href) > 2048 {
			return ""
		}
		target, err := url.Parse(href)
		if err != nil {
			continue
		}
		target = base.ResolveReference(target)
		if !officialProfileOrigin(target) || target.Path != "/user/profile/"+authorID || target.Fragment != "" {
			continue
		}
		query, err := url.ParseQuery(target.RawQuery)
		values := query["xsec_token"]
		if err != nil || len(values) > 1 {
			return ""
		}
		if len(values) == 0 || values[0] == "" {
			continue
		}
		value := values[0]
		if strings.ContainsAny(value, "\r\n\t ") || (token != "" && token != value) {
			return ""
		}
		token = value
	}
	return token
}

func officialProfileOrigin(value *url.URL) bool {
	return value.Scheme == "https" && value.User == nil &&
		(value.Host == "www.xiaohongshu.com" || value.Host == "xiaohongshu.com")
}
