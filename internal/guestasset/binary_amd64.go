//go:build amd64

package guestasset

import _ "embed"

//go:embed deepcove-guest-init-amd64
var binary []byte
