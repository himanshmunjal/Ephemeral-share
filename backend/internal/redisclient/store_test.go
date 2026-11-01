package redisclient

import "testing"

func TestOptionsEnableTLSOnlyWhenAsked(t *testing.T) {
	if Options("host:6379", "pw", false).TLSConfig != nil {
		t.Error("TLS must be off by default (local/internal Redis)")
	}
	o := Options("host:6379", "pw", true)
	if o.TLSConfig == nil || o.TLSConfig.MinVersion < 0x0303 {
		t.Errorf("TLS config missing or too permissive: %+v", o.TLSConfig)
	}
	if o.Password != "pw" || o.Addr != "host:6379" {
		t.Error("addr/password not carried through")
	}
}
