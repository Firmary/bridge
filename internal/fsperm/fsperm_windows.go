// Package fsperm checks that the Link's data directory and key file belong to the person running it.
package fsperm

import "io/fs"

// CheckOwned is not implemented on Windows yet: the owner SID and ACL check arrives with the desktop installers
// (S15). %LOCALAPPDATA% already inherits a user-only ACL.
func CheckOwned(path string, forbidden fs.FileMode) error { return nil }
