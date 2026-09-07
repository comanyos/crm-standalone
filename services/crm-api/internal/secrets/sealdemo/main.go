// Command sealdemo seals a lead-capture signing secret exactly the way the
// running service does.
//
// It exists so a live end-to-end delivery can be verified against the real
// Compose stack without a bootstrapped organization to create an endpoint
// through the authenticated API — the sealing binding is the one thing a
// psql insert cannot produce on its own. It is a development aid, not part
// of the service: nothing imports it and it is not built into the image.
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/santoshguru/crm/internal/integration/leadcapture"
	"github.com/santoshguru/crm/internal/secrets"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: sealdemo <base64-key> <tenant> <endpoint-id> <secret>")
		os.Exit(2)
	}
	key, tenant, endpointID, secret := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	sealer, err := secrets.New(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sealed, err := sealer.Seal([]byte(secret), leadcapture.AssociatedData(tenant, endpointID))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// PostgreSQL bytea hex format, ready to paste into psql.
	fmt.Print(`\x` + hex.EncodeToString(sealed))
}
