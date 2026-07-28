// init.go — register the QUIC transport factory with api.
//
// The factory looks up a pre-registered certificate via
// RegisterCertificate (keyed by opts.TLSCertFile).  Callers that
// already hold a *tls.Certificate can construct a quic.Transport
// directly via quic.NewTransport.
package quic

import (
	"crypto/tls"
	"sync"

	"github.com/horreum/horreum/internal/transport/api"
)

var (
	certRegistryMu sync.Mutex
	certRegistry   = map[string]*tls.Certificate{}
)

// RegisterCertificate stores cert under path so the api factory can
// find it when constructing the transport.
func RegisterCertificate(path string, cert *tls.Certificate) {
	certRegistryMu.Lock()
	defer certRegistryMu.Unlock()
	certRegistry[path] = cert
}

func loadCertificate(path string) *tls.Certificate {
	certRegistryMu.Lock()
	defer certRegistryMu.Unlock()
	return certRegistry[path]
}

func init() {
	api.RegisterTransport("quic", func(opts api.Options) (api.Transport, error) {
		cert := loadCertificate(opts.TLSCertFile)
		if cert == nil {
			return nil, errNoCert
		}
		return NewTransport(opts.Addr, opts.Router, cert)
	})
}
