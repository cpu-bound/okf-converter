package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{"correct password verifies", "correct-horse-battery-staple", true},
		{"wrong password fails", "wrong-password", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := VerifyPassword(hash, tt.password)
			if err != nil {
				t.Fatalf("VerifyPassword: %v", err)
			}
			if ok != tt.want {
				t.Errorf("VerifyPassword() = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	_, err := VerifyPassword("not-a-valid-hash", "anything")
	if err == nil {
		t.Fatal("expected error for malformed encoded hash, got nil")
	}
}

func TestHashPasswordProducesUniqueSalts(t *testing.T) {
	h1, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	h2, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h1 == h2 {
		t.Error("expected different encoded hashes for the same password due to random salts")
	}
}
