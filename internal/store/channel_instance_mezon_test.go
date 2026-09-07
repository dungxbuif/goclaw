package store

import "testing"

func TestMezonDefaultChannelInstance(t *testing.T) {
	if !IsDefaultChannelInstance("mezon") || !IsDefaultChannelInstance("mezon/default") {
		t.Fatal("mezon default instance naming is not recognized")
	}
}
