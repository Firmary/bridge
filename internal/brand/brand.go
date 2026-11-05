// Package brand holds the product name people see, and the names derived from it. Change them here and nowhere
// else.
//
// Copy uses Name. Identifiers that must not move when the name changes stay where they are: the names an install
// leaves on the computer (Command, BundleID, InstallName), the protocol context strings that Firmary's relay also
// signs and checks (devicekey.AuthContext, the pairing contexts) and the relay protocol's field names.
package brand

// Name is the product name in every message the app shows a person.
const Name = "Firmary Bridge"

// Command is the executable's name (cmd/firmary-link), and the Linux data directory, package and service unit name.
// It kept the Firmary Link name so installed copies, their services and scripts keep working.
const Command = "firmary-link"

// BundleID is the macOS bundle identifier and launchd label.
const BundleID = "app.firmary.link"

// InstallName is the name the Firmary Link releases (0.2.0 to 0.3.1) gave what an install leaves on the computer:
// the macOS data folder, and the Windows logon task, Run value and install folder. It stays, so an installed copy
// upgrades in place: the updater keeps new versions in the data folder, and the copy installed first reads it to
// find them.
const InstallName = "Firmary Link"

// Former names, for moving an existing install's data directory on first run (see package datadir).
const (
	FormerName    = "Firmary Helper"
	FormerCommand = "firmary-helper"
)
