// shim.go — small shim so we can pass a *tls.Certificate through
// Options without importing crypto/tls at the transport package
// level (which would force callers into an unwanted TLS dep even
// when using TCP-only).
package transport

import "crypto/tls"

// tlsCertShim is just *tls.Certificate; defined separately so the
// transport package signature can refer to it without importing
// crypto/tls at the top level.
type tlsCertShim = tls.Certificate
