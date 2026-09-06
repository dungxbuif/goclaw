package http

import "testing"

func TestMezonIsValidChannelType(t *testing.T) {
	if !isValidChannelType("mezon") {
		t.Fatal("mezon channel type rejected by HTTP API")
	}
}
