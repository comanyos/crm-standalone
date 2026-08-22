package util

// SafeReturnTo accepts only relative paths (starting with "/" but not "//" or "/\")
// to prevent open-redirect attacks. Browsers normalise both "//" and "/\" to a
// protocol-relative URL, so both second-character variants must be rejected.
func SafeReturnTo(s string) string {
	if len(s) > 0 && s[0] == '/' && (len(s) < 2 || (s[1] != '/' && s[1] != '\\')) {
		return s
	}
	return ""
}
