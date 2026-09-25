package main

import "testing"

func TestValidateServerPassphrase(t *testing.T) {
	tests := []struct {
		name          string
		passphrase    string
		allowInsecure bool
		wantErr       bool
	}{
		{name: "passphrase set", passphrase: "0123456789", wantErr: false},
		{name: "empty passphrase rejected", passphrase: "", wantErr: true},
		{name: "empty passphrase with insecure", passphrase: "", allowInsecure: true, wantErr: false},
		{name: "passphrase with insecure", passphrase: "0123456789", allowInsecure: true, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServerPassphrase(tt.passphrase, tt.allowInsecure)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateServerPassphrase(%q, %v) error = %v, wantErr %v", tt.passphrase, tt.allowInsecure, err, tt.wantErr)
			}
		})
	}
}
