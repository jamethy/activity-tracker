#!/bin/bash -xe

# relies on stuff in .goreleaser.yaml

VERSION=$1
BUCKET=activity-tracker-lambda-artifacts
KEY=releases/activity-tracker_${VERSION}_linux_amd64.zip

# I named the lambda daily-tracker before renaming the repo and I'm too lazy to fix it
aws lambda update-function-code \
  --function-name daily-tracker \
  --s3-bucket $BUCKET \
  --s3-key $KEY