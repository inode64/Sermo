package conn

import (
	"errors"
	"testing"

	"github.com/digitalocean/go-libvirt"
)

type fakeLibvirtNetworkReader struct {
	active              int32
	lookupErr, stateErr error
}

func (f fakeLibvirtNetworkReader) NetworkLookupByName(name string) (libvirt.Network, error) {
	return libvirt.Network{Name: name}, f.lookupErr
}
func (f fakeLibvirtNetworkReader) NetworkIsActive(libvirt.Network) (int32, error) {
	return f.active, f.stateErr
}

func TestLibvirtNetworkActive(t *testing.T) {
	for _, tt := range []struct {
		name          string
		client        fakeLibvirtNetworkReader
		want, wantErr bool
	}{
		{name: "active", client: fakeLibvirtNetworkReader{active: 1}, want: true},
		{name: "inactive"},
		{name: "missing", client: fakeLibvirtNetworkReader{lookupErr: errors.New("missing network")}, wantErr: true},
		{name: "unreadable", client: fakeLibvirtNetworkReader{stateErr: errors.New("denied")}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := libvirtNetworkActive(tt.client, "test-network")
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %v, %v; want %v, error=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
