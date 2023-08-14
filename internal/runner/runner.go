package runner

import (
	"io"
	"net"
)

type Runner interface {
	Start() error
	Wait() error
	Kill() error
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	ResolveAddr(network, address string) (net.Addr, error)
	Name() string
	ID() string
}
