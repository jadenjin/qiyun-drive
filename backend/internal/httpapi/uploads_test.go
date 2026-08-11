package httpapi

import "testing"

func TestCleanRelativePathRejectsTraversal(t *testing.T) {
	invalid := []string{"../secret.txt", "/root/file", "folder/../../secret", "folder/bad:name"}
	for _, value := range invalid {
		if _, err := cleanRelativePath(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestCleanRelativePathNormalizesWindowsSeparators(t *testing.T) {
	parts, err := cleanRelativePath(`旅行照片\2026\山顶.heic`)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0] != "旅行照片" || parts[2] != "山顶.heic" {
		t.Fatalf("unexpected parts: %#v", parts)
	}
}

func TestChoosePartSizeStaysWithinMultipartLimit(t *testing.T) {
	partSize := choosePartSize(500 * 1024 * 1024 * 1024)
	parts := (500*1024*1024*1024 + partSize - 1) / partSize
	if partSize < 16*1024*1024 {
		t.Fatalf("part is too small: %d", partSize)
	}
	if parts > 10000 {
		t.Fatalf("too many parts: %d", parts)
	}
}

func TestMaximumObjectSizeStaysWithinMultipartLimit(t *testing.T) {
	partSize := choosePartSize(maxObjectSize)
	parts := (maxObjectSize + partSize - 1) / partSize
	if parts > 10000 {
		t.Fatalf("maximum object requires too many parts: %d", parts)
	}
}

func TestSupportedPhotoMimeTypes(t *testing.T) {
	for _, value := range []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/heic", "image/heif"} {
		if !isPhotoMime(value) {
			t.Fatalf("expected %s to be recognized", value)
		}
	}
	if isPhotoMime("video/mp4") {
		t.Fatal("videos are not part of the v1 photo timeline")
	}
}

func TestInferHEICMimeWhenBrowserDoesNotProvideOne(t *testing.T) {
	if got := inferMimeType("IMG_1024.HEIC", "application/octet-stream"); got != "image/heic" {
		t.Fatalf("unexpected mime type: %s", got)
	}
}
