// init.go — register the TCP transport factory with api.
package tcp

import "github.com/horreum/horreum/internal/transport/api"

func init() {
	api.RegisterTransport("tcp", func(opts api.Options) (api.Transport, error) {
		return NewTransport(opts.Addr, opts.Router, opts.Recorder)
	})
}
