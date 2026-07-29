// Package quic — error sentinels.
package quic

import "errors"

var errNoCert = errors.New("transport/quic: no certificate registered; call quic.RegisterCertificate first")
