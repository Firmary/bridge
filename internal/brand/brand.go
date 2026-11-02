// Package brand holds the product name people see. The name is not final: change it here and nowhere else.
//
// Copy uses it, and so does the macOS data folder, which Finder shows by name. Identifiers that must not move when
// the name changes stay where they are: the command name (cmd/firmary-helper), the Windows and Linux data
// directories, and the protocol context strings that Firmary's relay also signs and checks (devicekey.AuthContext,
// the pairing contexts).
package brand

// Name is the product name in every message the Helper shows a person.
const Name = "Firmary Helper"
