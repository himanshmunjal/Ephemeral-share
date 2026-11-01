package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Client wraps an S3-compatible client (works with AWS S3, Cloudflare R2, or MinIO).
type Client struct {
	s3      *s3.Client        // internal endpoint: used for server-side calls (list/delete)
	presign *s3.PresignClient // public endpoint: URLs handed to the browser
	bucket  string
}

// New builds a storage client. `endpoint` is the address the backend itself
// uses to reach the object store (e.g. the docker-compose service name
// "http://minio:9000"). `publicEndpoint` is the address that must be
// reachable from the end user's browser (e.g. "http://localhost:9000", or a
// public bucket/CDN URL) and is used only for signing pre-signed PUT/GET
// URLs — those two are frequently NOT the same host, and mixing them up is
// why direct-to-browser uploads/downloads fail with an unreachable host even
// though the backend's own S3 calls work fine. If publicEndpoint is blank it
// falls back to endpoint.
func New(ctx context.Context, endpoint, publicEndpoint, region, accessKey, secretKey, bucket string) (*Client, error) {
	if publicEndpoint == "" {
		publicEndpoint = endpoint
	}

	var optFns []func(*config.LoadOptions) error
	optFns = append(optFns, config.WithRegion(region))

	// If explicit credentials were provided, use them (local MinIO, or an IAM
	// user's static keys). If left blank, fall back to the SDK's default
	// credential chain — e.g. an IAM role attached to the ECS task/EC2
	// instance — which is the recommended path for a real cloud deployment.
	if accessKey != "" && secretKey != "" {
		optFns = append(optFns, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	cfg, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, err
	}

	withEndpoint := func(ep string) func(o *s3.Options) {
		return func(o *s3.Options) {
			// A custom endpoint means we're talking to MinIO/R2, which requires
			// path-style addressing. "none" (real AWS S3) uses the SDK's default
			// endpoint resolution and virtual-hosted-style requests.
			if ep != "" && ep != "none" {
				o.BaseEndpoint = aws.String(ep)
				o.UsePathStyle = true
			}
		}
	}

	internal := s3.NewFromConfig(cfg, withEndpoint(endpoint))
	// A distinct client (same credentials, different BaseEndpoint) purely so
	// its PresignClient signs URLs against the publicly-reachable host.
	public := s3.NewFromConfig(cfg, withEndpoint(publicEndpoint))

	return &Client{s3: internal, presign: s3.NewPresignClient(public), bucket: bucket}, nil
}

func objectKey(sessionCode, contentID, filename string) string {
	return fmt.Sprintf("sessions/%s/%s/%s", sessionCode, contentID, filename)
}

// PresignPutURL returns a short-lived URL the client uploads directly to,
// keeping large binaries off the API server entirely.
func (c *Client) PresignPutURL(ctx context.Context, sessionCode, contentID, filename, contentType string, size int64, signLength bool, expiresIn time.Duration) (string, string, error) {
	key := objectKey(sessionCode, contentID, filename)
	in := &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}
	if signLength {
		// Signed into the URL: object storage rejects a PUT whose body length
		// differs, so the size the quota was reserved for can't be exceeded.
		// When disabled, CompleteUpload's size check (and deletion on
		// mismatch) is the only enforcement.
		in.ContentLength = aws.Int64(size)
	}
	req, err := c.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(expiresIn))
	if err != nil {
		return "", "", err
	}
	return req.URL, key, nil
}

// PresignGetURL returns a short-lived download URL for a stored object.
func (c *Client) PresignGetURL(ctx context.Context, storageKey string, expiresIn time.Duration) (string, error) {
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storageKey),
		// Never let the browser render a stored object inline: force a
		// download of opaque bytes.
		ResponseContentDisposition: aws.String("attachment"),
		ResponseContentType:        aws.String("application/octet-stream"),
	}, s3.WithPresignExpires(expiresIn))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// ObjectSize returns the stored size of an object, or an error if it doesn't
// exist (e.g. the browser's direct upload never finished).
func (c *Client) ObjectSize(ctx context.Context, storageKey string) (int64, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storageKey),
	})
	if err != nil {
		return 0, err
	}
	return aws.ToInt64(out.ContentLength), nil
}

// DeleteObject removes a single object (used by session cleanup).
func (c *Client) DeleteObject(ctx context.Context, storageKey string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(storageKey),
	})
	return err
}

// DeleteSessionObjects removes every object under a session's prefix (bulk
// cleanup), following pagination so sessions with >1000 objects are fully purged.
func (c *Client) DeleteSessionObjects(ctx context.Context, sessionCode string) error {
	prefix := fmt.Sprintf("sessions/%s/", sessionCode)
	p := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if err := c.DeleteObject(ctx, aws.ToString(obj.Key)); err != nil {
				return err
			}
		}
	}
	return nil
}

// StoredObject is one entry returned by ListSessionObjects.
type StoredObject struct {
	Key          string
	SessionCode  string
	LastModified time.Time
}

// ListSessionObjects walks every object under "sessions/" so the orphan sweep
// can compare them with live sessions.
func (c *Client) ListSessionObjects(ctx context.Context) ([]StoredObject, error) {
	var out []StoredObject
	p := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String("sessions/"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			parts := strings.SplitN(key, "/", 3)
			if len(parts) < 3 {
				continue
			}
			out = append(out, StoredObject{Key: key, SessionCode: parts[1], LastModified: aws.ToTime(obj.LastModified)})
		}
	}
	return out, nil
}

// Ping verifies the bucket is reachable (used by deep health checks).
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	return err
}

// CountObjectVersions returns how many versions (including hidden/delete
// markers) exist under a prefix. Stores that keep old versions (Backblaze B2's
// default) keep counting them against quota after a delete, so the pre-flight
// check uses this to tell the operator when a lifecycle rule is needed.
func (c *Client) CountObjectVersions(ctx context.Context, prefix string) (int, error) {
	n := 0
	p := s3.NewListObjectVersionsPaginator(c.s3, &s3.ListObjectVersionsInput{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		n += len(page.Versions) + len(page.DeleteMarkers)
	}
	return n, nil
}

// CORSRule is a simplified view of one bucket CORS rule.
type CORSRule struct {
	Origins, Methods, Headers, Expose []string
	MaxAgeSeconds                     int32
}

// GetBucketCORS reads the bucket's current CORS rules (for diagnostics).
func (c *Client) GetBucketCORS(ctx context.Context) ([]CORSRule, error) {
	out, err := c.s3.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		return nil, err
	}
	rules := make([]CORSRule, 0, len(out.CORSRules))
	for _, r := range out.CORSRules {
		rules = append(rules, CORSRule{
			Origins: r.AllowedOrigins, Methods: r.AllowedMethods, Headers: r.AllowedHeaders,
			Expose: r.ExposeHeaders, MaxAgeSeconds: aws.ToInt32(r.MaxAgeSeconds),
		})
	}
	return rules, nil
}

// SetBucketCORS replaces the bucket's CORS rules with one rule that lets the
// given origins upload (PUT), download (GET) and probe (HEAD) directly from the
// browser using pre-signed URLs.
func (c *Client) SetBucketCORS(ctx context.Context, origins []string) error {
	_, err := c.s3.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(c.bucket),
		CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{{
			AllowedOrigins: origins,
			AllowedMethods: []string{"GET", "PUT", "HEAD"},
			AllowedHeaders: []string{"*"},
			ExposeHeaders:  []string{"ETag"},
			MaxAgeSeconds:  aws.Int32(3000),
		}}},
	})
	return err
}
