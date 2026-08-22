# Platform contract release snapshot

This directory is a source-compatible release snapshot of the canonical
CompanyOS platform contract. It exists so the AuthinCore repository, container,
and source archive build without relying on a sibling monorepo checkout.

When the canonical contract is published as a tagged Go module, replace this
snapshot with that immutable version and remove the local `replace` directive.
