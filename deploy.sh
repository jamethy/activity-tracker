#!/bin/bash -xe

VERSION=$1
BUCKET=jamesianburns-random-data
KEY=daily-tracker/daily-tracker-lambda-$VERSION.zip

git tag $VERSION

export AWS_PROFILE=personal

CGO_ENABLED=0 go build -o bootstrap -ldflags "-X main.version=$VERSION" .
zip -r daily-tracker-lambda-$VERSION.zip bootstrap app_templ.txt
aws s3 cp daily-tracker-lambda-$VERSION.zip s3://$BUCKET/$KEY

aws lambda update-function-code \
  --function-name daily-tracker \
  --s3-bucket $BUCKET \
  --s3-key $KEY