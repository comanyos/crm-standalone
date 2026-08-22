package webauthn

import (
	"bytes"
	"encoding/json"
	"io"
)

func jsonReader(raw json.RawMessage) io.Reader {
	return bytes.NewReader(raw)
}
