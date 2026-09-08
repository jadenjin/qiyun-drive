package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var ErrIntegrity = errors.New("upload integrity validation failed")

// Seal copies one consistent source version into a key that has never been
// exposed by a PUT capability. Every range uses the same source ETag; a replay
// between multipart-copy ranges fails rather than publishing mixed versions.
func (s *Store) Seal(ctx context.Context, source, destination string, expected int64) (string, error) {
	if source == destination {
		return "", fmt.Errorf("immutable destination must differ from staging key")
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(source)})
	if err != nil {
		return "", err
	}
	if aws.ToInt64(head.ContentLength) != expected || aws.ToString(head.ETag) == "" {
		return "", fmt.Errorf("%w: staging object size or identity mismatch", ErrIntegrity)
	}
	copySource := url.PathEscape(s.bucket + "/" + source)
	const copyPartSize int64 = 4 << 30
	if expected <= copyPartSize {
		_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(destination),
			CopySource: aws.String(copySource), CopySourceIfMatch: head.ETag,
		})
	} else {
		var uploadID string
		uploadID, err = s.CreateMultipart(ctx, destination, aws.ToString(head.ContentType))
		if err != nil {
			return "", err
		}
		finished := false
		defer func() {
			if !finished {
				// Keep cleanup independent of a cancelled transfer context.
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				_ = s.AbortMultipart(cleanup, destination, uploadID)
			}
		}()
		parts := make([]CompletedPart, 0)
		var number int32 = 1
		for start := int64(0); start < expected; start += copyPartSize {
			if number > 10000 {
				return "", fmt.Errorf("%w: too many publication parts", ErrIntegrity)
			}
			part, copyErr := s.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
				Bucket: aws.String(s.bucket), Key: aws.String(destination), UploadId: aws.String(uploadID), PartNumber: aws.Int32(number),
				CopySource: aws.String(copySource), CopySourceIfMatch: head.ETag,
				CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", start, min(expected, start+copyPartSize)-1)),
			})
			if copyErr != nil {
				return "", copyErr
			}
			if part.CopyPartResult == nil || aws.ToString(part.CopyPartResult.ETag) == "" {
				return "", fmt.Errorf("missing copy ETag")
			}
			parts = append(parts, CompletedPart{Number: number, ETag: aws.ToString(part.CopyPartResult.ETag)})
			number++
		}
		err = s.CompleteMultipart(ctx, destination, uploadID, parts)
		finished = err == nil
	}
	if err != nil {
		return "", err
	}
	body, err := s.Get(ctx, destination)
	if err != nil {
		return "", err
	}
	defer body.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(body, expected+1))
	if err != nil {
		return "", err
	}
	if n != expected {
		return "", fmt.Errorf("%w: published object size mismatch", ErrIntegrity)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
