#!/bin/sh
# Provisions the ephemeral-share bucket: public downloads (content is already
# gated by the session code/TTL at the API layer) and, critically, a CORS
# policy — without it the browser's direct-to-MinIO presigned PUT uploads are
# blocked cross-origin and every upload fails client-side.
set -e

mc alias set local http://minio:9000 minioadmin minioadmin
mc mb -p local/ephemeral-share || true
mc anonymous set download local/ephemeral-share || true

cat > /tmp/cors.json <<'EOF'
{
  "CORSRules": [
    {
      "AllowedOrigin": ["*"],
      "AllowedMethod": ["GET", "PUT", "HEAD"],
      "AllowedHeader": ["*"],
      "ExposeHeader": ["ETag"],
      "MaxAgeSeconds": 3000
    }
  ]
}
EOF
mc cors set local/ephemeral-share /tmp/cors.json || echo "CORS not set (unsupported by this MinIO build; server-wide CORS applies)"
