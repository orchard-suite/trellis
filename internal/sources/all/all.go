// Package all brings every source trellis ships into the registry. The
// binary imports it once; nothing else names a source type.
package all

import (
	_ "github.com/orchard-suite/trellis/internal/sources/alertrules"
	_ "github.com/orchard-suite/trellis/internal/sources/azuregraph"
	_ "github.com/orchard-suite/trellis/internal/sources/azurepolicy"
	_ "github.com/orchard-suite/trellis/internal/sources/command"
	_ "github.com/orchard-suite/trellis/internal/sources/definitions"
	_ "github.com/orchard-suite/trellis/internal/sources/github"
	_ "github.com/orchard-suite/trellis/internal/sources/graft"
	_ "github.com/orchard-suite/trellis/internal/sources/keyvault"
	_ "github.com/orchard-suite/trellis/internal/sources/kubernetes"
	_ "github.com/orchard-suite/trellis/internal/sources/orrery"
	_ "github.com/orchard-suite/trellis/internal/sources/signing"
	_ "github.com/orchard-suite/trellis/internal/sources/telemetry"
	_ "github.com/orchard-suite/trellis/internal/sources/waivers"
)
