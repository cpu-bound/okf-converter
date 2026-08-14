package auth

import "testing"

func TestCreateAndParseTokenRoundTrip(t *testing.T) {
	user := User{ID: "user-123", Name: "Ada", Email: "ada@example.com"}

	token, err := CreateToken(user, "test-secret")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	userID, err := ParseUserID(token, "test-secret")
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}

	if userID != user.ID {
		t.Errorf("ParseUserID() = %q, want %q", userID, user.ID)
	}
}

func TestParseUserIDWrongSecret(t *testing.T) {
	token, err := CreateToken(User{ID: "user-123"}, "test-secret")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if _, err := ParseUserID(token, "wrong-secret"); err == nil {
		t.Fatal("expected error when parsing with the wrong secret, got nil")
	}
}

func TestParseUserIDGarbage(t *testing.T) {
	if _, err := ParseUserID("not-a-jwt", "test-secret"); err == nil {
		t.Fatal("expected error for garbage token, got nil")
	}
}
