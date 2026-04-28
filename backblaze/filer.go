// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package backblaze

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sync"

	"github.com/kothar/go-backblaze"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

var _ data.Filer = (*Filer)(nil)

// Filer uploads files to a Backblaze B2 bucket and returns the
// public URL. The bucket must be public-read for the URLs to
// work without further authentication.
type Filer struct {
	bucketName  string
	prefix      string
	downloadURL string

	mu     sync.Mutex
	bucket *backblaze.Bucket
	client *backblaze.B2
}

// NewFiler returns a Filer for the given bucket and key prefix.
// Credentials are read from viper lazily on first upload.
func NewFiler(bucketName, prefix string) *Filer {
	prefix = path.Clean(prefix)
	if prefix == "." || prefix == "/" {
		prefix = ""
	}

	return &Filer{bucketName: bucketName, prefix: prefix}
}

// NewFilerForTest skips the lazy authorise step so unit tests can
// exercise URL construction without contacting B2.
func NewFilerForTest(bucketName, prefix, downloadURL string) *Filer {
	prefix = path.Clean(prefix)
	if prefix == "." || prefix == "/" {
		prefix = ""
	}

	return &Filer{bucketName: bucketName, prefix: prefix, downloadURL: downloadURL}
}

// PublicURL returns the public-read URL the configured bucket
// serves the named file from.
func (f *Filer) PublicURL(name string) string {
	parts := []string{"file", f.bucketName}
	if f.prefix != "" {
		parts = append(parts, f.prefix)
	}

	parts = append(parts, name)

	joined, _ := url.JoinPath(f.downloadURL, parts...)

	return joined
}

// CreateFile uploads data to <bucket>/<prefix>/<name> and returns
// the public URL.
func (f *Filer) CreateFile(name string, data []byte) (string, error) {
	if err := f.ensureAuthorised(); err != nil {
		return "", err
	}

	key := name
	if f.prefix != "" {
		key = fmt.Sprintf("%s/%s", f.prefix, name)
	}

	if _, err := f.bucket.UploadFile(key, nil, bytes.NewReader(data)); err != nil {
		log.Error().Err(err).Str("Bucket", f.bucketName).Str("Key", key).Msg("backblaze upload failed")

		return "", err
	}

	return f.PublicURL(name), nil
}

func (f *Filer) ensureAuthorised() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.bucket != nil {
		return nil
	}

	keyID := viper.GetString("backblaze.application_id")
	appKey := viper.GetString("backblaze.application_key")

	if keyID == "" || appKey == "" {
		return errors.New("backblaze: application_id / application_key not configured")
	}

	client, err := backblaze.NewB2(backblaze.Credentials{KeyID: keyID, ApplicationKey: appKey})
	if err != nil {
		return fmt.Errorf("backblaze authorise: %w", err)
	}

	bucket, err := client.Bucket(f.bucketName)
	if err != nil {
		return fmt.Errorf("backblaze bucket %q lookup: %w", f.bucketName, err)
	}

	if bucket == nil {
		return fmt.Errorf("backblaze bucket %q not found", f.bucketName)
	}

	downloadURL, err := client.DownloadURL()
	if err != nil {
		return fmt.Errorf("backblaze download url: %w", err)
	}

	f.client = client
	f.bucket = bucket
	f.downloadURL = downloadURL

	return nil
}
