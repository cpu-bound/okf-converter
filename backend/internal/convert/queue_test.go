package convert

import "testing"

func TestDecodeJob(t *testing.T) {
	body := []byte(`{"FileID":"file-1","ObjectKey":"user-1/src.txt","ContentType":"text/plain","OriginalName":"notes.txt"}`)

	job, err := decodeJob(body)
	if err != nil {
		t.Fatalf("decodeJob() error = %v", err)
	}

	want := Job{FileID: "file-1", ObjectKey: "user-1/src.txt", ContentType: "text/plain", OriginalName: "notes.txt"}
	if job != want {
		t.Errorf("decodeJob() = %#v, want %#v", job, want)
	}
}

func TestDecodeJobInvalidJSON(t *testing.T) {
	if _, err := decodeJob([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
