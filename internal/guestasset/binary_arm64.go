//go:build arm64

package guestasset

import _ "embed"

//go:embed deepcove-guest-init-arm64
var binary []byte
