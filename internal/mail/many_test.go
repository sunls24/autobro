package mail

import "testing"

func TestNewManyMeRequiresConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		username       string
		forwardAddress string
		wantErr        string
	}{
		{
			name:           "missing username",
			username:       " ",
			forwardAddress: "target@example.com",
			wantErr:        "MANYME_USERNAME is required",
		},
		{
			name:           "missing forward address",
			username:       "username",
			forwardAddress: " ",
			wantErr:        "MANYME_FORWARD_ADDRESS is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewManyMe(tt.username, tt.forwardAddress)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("NewManyMe() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
