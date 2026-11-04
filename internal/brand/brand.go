// Package brand holds the product name people see, and the names derived from it. Change them here and nowhere
// else.
//
// Copy uses Name, and so do the macOS data folder, which Finder shows by name, and the login item. Identifiers that
// must not move when the name changes stay where they are: the protocol context strings that Firmary's relay also
// signs and checks (devicekey.AuthContext, the pairing contexts) and the relay protocol's field names.
package brand

// Name is the product name in every message the Link shows a person.
const Name = "Firmary Link"

// Command is the executable's name (cmd/firmary-link), and the Linux data directory and service unit name.
const Command = "firmary-link"

// BundleID is the macOS bundle identifier and launchd label.
const BundleID = "app.firmary.link"

// Former names, for moving an existing install's data directory on first run (see package datadir).
const (
	FormerName    = "Firmary Helper"
	FormerCommand = "firmary-helper"
)
