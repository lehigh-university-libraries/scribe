package store

import "testing"

func TestValidateImageStorageReference(t *testing.T) {
	for name, test := range map[string]struct {
		imageURL     string
		storageBytes uint64
		wantError    bool
	}{
		"owned upload":           {imageURL: "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg", storageBytes: 80},
		"owned upload no size":   {imageURL: "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg", wantError: true},
		"external image":         {imageURL: "https://images.example/owned.jpg"},
		"external image size":    {imageURL: "https://images.example/owned.jpg", storageBytes: 80, wantError: true},
		"external matching path": {imageURL: "https://images.example/static/uploads/owned.jpg", storageBytes: 80, wantError: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateImageStorageReference(test.imageURL, test.storageBytes)
			if (err != nil) != test.wantError {
				t.Fatalf("validateImageStorageReference(%q, %d) error = %v, wantError %v", test.imageURL, test.storageBytes, err, test.wantError)
			}
		})
	}
}
